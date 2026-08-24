package edgecore

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yufeng/lib/kernel"
)

func TestReleaseProxyPreservesRequestAndUpstreamStatus(t *testing.T) {
	var calls atomic.Int64
	var gotHost, gotForwardedFor, gotForwardedHost, gotForwardedProto string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotHost = r.Host
		gotForwardedFor = r.Header.Get("X-Forwarded-For")
		gotForwardedHost = r.Header.Get("X-Forwarded-Host")
		gotForwardedProto = r.Header.Get("X-Forwarded-Proto")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.Header().Set("X-Upstream-Result", "preserved")
		if r.URL.Path == "/status" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = fmt.Fprintf(w, "%s|%s|%s", r.URL.RawQuery, r.Header.Get("X-Protocol-Proof"), body)
	}))
	t.Cleanup(upstream.Close)
	upstreamURL := mustParseURL(t, upstream.URL)
	proxy := NewReleaseProxy(NewReleaseSet(), nil, upstreamURL, "asset-a")
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/status?a=1&a=2", strings.NewReader("body=hello+world"))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "shop.example"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req.Header.Set("X-Protocol-Proof", "alpha")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读测试响应在断言完成后尽力清理。
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("X-Upstream-Result") != "preserved" {
		t.Fatalf("status=%d header=%q", resp.StatusCode, resp.Header.Get("X-Upstream-Result"))
	}
	if string(body) != "a=1&a=2|alpha|body=hello+world" || calls.Load() != 1 {
		t.Fatalf("body=%q calls=%d", body, calls.Load())
	}
	if gotHost != upstreamURL.Host {
		t.Fatalf("upstream host=%q want=%q", gotHost, upstreamURL.Host)
	}
	if strings.Contains(gotForwardedFor, "198.51.100.7") || gotForwardedFor == "" {
		t.Fatalf("forwarded for=%q must replace untrusted input with direct peer", gotForwardedFor)
	}
	if gotForwardedHost != "shop.example" || gotForwardedProto != "http" {
		t.Fatalf("forwarded host=%q proto=%q", gotForwardedHost, gotForwardedProto)
	}
}

func TestReleaseProxyRejectsIncompleteInspectionBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(upstream.Close)
	proxy := NewReleaseProxy(NewReleaseSet(), nil, mustParseURL(t, upstream.URL), "asset-a")

	malformed := httptest.NewRequest(http.MethodGet, "http://edge.invalid/items", nil)
	malformed.URL.RawQuery = "id=%zz"
	malformedResult := httptest.NewRecorder()
	proxy.ServeHTTP(malformedResult, malformed)
	if malformedResult.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d", malformedResult.Code)
	}

	oversized := httptest.NewRequest(http.MethodPost, "http://edge.invalid/upload", bytes.NewReader(bytes.Repeat([]byte("x"), kernel.EngineBodyLimitBytes+1)))
	oversizedResult := httptest.NewRecorder()
	proxy.ServeHTTP(oversizedResult, oversized)
	if oversizedResult.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d", oversizedResult.Code)
	}
	if calls.Load() != 0 {
		t.Fatalf("incomplete requests reached upstream %d times", calls.Load())
	}
}

func TestReleaseProxyFailsClosedOnOverloadAndUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	upstreamURL := mustParseURL(t, upstream.URL)
	upstream.Close()
	proxy := NewReleaseProxy(NewReleaseSet(), nil, upstreamURL, "asset-a")

	failure := httptest.NewRecorder()
	proxy.ServeHTTP(failure, httptest.NewRequest(http.MethodGet, "http://edge.invalid/", nil))
	if failure.Code != http.StatusBadGateway || failure.Body.String() != "{\"error\":\"upstream unavailable\"}\n" {
		t.Fatalf("upstream failure status=%d body=%q", failure.Code, failure.Body.String())
	}

	proxy.inflight.Store(int64(kernel.EdgeInFlight))
	overload := httptest.NewRecorder()
	proxy.ServeHTTP(overload, httptest.NewRequest(http.MethodGet, "http://edge.invalid/", nil))
	if overload.Code != http.StatusServiceUnavailable {
		t.Fatalf("overload status=%d", overload.Code)
	}
}

func TestReleaseProxyTunnelsApprovedUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "yufeng-echo") {
			http.Error(w, "missing upgrade", http.StatusBadRequest)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("upstream response writer cannot hijack")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close() //nolint:errcheck // 升级回显完成后只做测试连接尽力清理。
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: yufeng-echo\r\n\r\n")
		_ = rw.Flush()
		line, err := rw.ReadString('\n')
		if err == nil {
			_, _ = rw.WriteString("echo:" + line)
			_ = rw.Flush()
		}
	}))
	t.Cleanup(upstream.Close)
	proxy := httptest.NewServer(NewReleaseProxy(NewReleaseSet(), nil, mustParseURL(t, upstream.URL), "asset-a"))
	t.Cleanup(proxy.Close)

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // 升级回归断言完成后只做测试连接尽力清理。
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(conn, "GET /upgrade HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: yufeng-echo\r\n\r\n", proxyURL.Host)
	reader := bufio.NewReader(conn)
	var response strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		response.WriteString(line)
		if line == "\r\n" {
			break
		}
	}
	if !strings.Contains(response.String(), "101 Switching Protocols") {
		t.Fatalf("upgrade response=%q", response.String())
	}
	_, _ = io.WriteString(conn, "ping\n")
	echo, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if echo != "echo:ping\n" {
		t.Fatalf("upgrade echo=%q", echo)
	}
}
