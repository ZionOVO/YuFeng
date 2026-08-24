// testupstream 回显真实反向代理链路的请求与上游状态；只用于部署验收。
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type echoResponse struct {
	Name     string              `json:"name"`
	Method   string              `json:"method"`
	Path     string              `json:"path"`
	RawQuery string              `json:"raw_query"`
	Header   map[string][]string `json:"header"`
	Body     string              `json:"body"`
}

func main() {
	addr := flag.String("addr", ":8080", "监听地址")
	name := flag.String("name", "app", "实例名")
	flag.Parse()
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler(*name),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

func handler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upgrade" && strings.EqualFold(r.Header.Get("Upgrade"), "yufeng-echo") {
			serveUpgrade(w, r)
			return
		}
		if delay, ok := responseDelay(r.URL.Path); ok {
			time.Sleep(delay)
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			http.Error(w, "read request body", http.StatusBadRequest)
			return
		}
		status := http.StatusOK
		if raw := strings.TrimPrefix(r.URL.Path, "/status/"); raw != r.URL.Path {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 100 && parsed <= 599 {
				status = parsed
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Name", name)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(echoResponse{
			Name: name, Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
			Header: r.Header.Clone(), Body: string(body),
		})
	})
}

func responseDelay(path string) (time.Duration, bool) {
	raw := strings.TrimPrefix(path, "/delay/")
	if raw == path {
		return 0, false
	}
	millis, err := strconv.Atoi(raw)
	if err != nil || millis < 1 || millis > 10_000 {
		return 0, false
	}
	return time.Duration(millis) * time.Millisecond, true
}

func serveUpgrade(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "upgrade is unavailable", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck // 升级回显结束后只做短连接尽力清理。
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: yufeng-echo\r\n\r\n")
	if err := rw.Flush(); err != nil {
		return
	}
	line, err := rw.ReadString('\n')
	if err != nil {
		return
	}
	_, _ = rw.WriteString("echo:" + line)
	_ = rw.Flush()
}
