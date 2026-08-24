package edgecore

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

// regexpHexID 匹配遥测里的事件 id（32 位十六进制，128 位随机）。
var regexpHexID = regexp.MustCompile(`"id":"[0-9a-f]{32}"`)

// 测试辅助：只服务本包测试，生产代码不得引用。

func newKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func newSignedArtifact(t *testing.T, priv ed25519.PrivateKey, rules []Rule) *artifactv1.Artifact {
	t.Helper()
	payload, err := MarshalRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Kind:          artifactv1.Kind_KIND_RULE,
		Payload:       payload,
		Ttl:           durationpb.New(time.Hour),
		CreatedAt:     timestamppb.Now(),
		CreatedBy:     "test",
		PayloadSchema: RulePayloadSchema,
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	return a
}

func mustDetector(t *testing.T, a *artifactv1.Artifact) Detector {
	t.Helper()
	rules, err := ParseRules(a.Payload)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewRuleDetector(a.Id, rules)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func newEchoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("upstream ok\n"))
	})
}

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func doReq(t *testing.T, base, target string) (int, string) {
	t.Helper()
	resp, err := http.Get(base + target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读测试响应在断言完成后尽力清理。
	buf := make([]byte, 128)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n])
}

func splitLines(s string) [][]byte {
	var out [][]byte
	for _, l := range bytes.Split([]byte(s), []byte("\n")) {
		if len(l) > 0 {
			out = append(out, l)
		}
	}
	return out
}
