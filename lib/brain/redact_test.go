package brain

import (
	"strings"
	"testing"
)

func TestRedactQueryDropsValues(t *testing.T) {
	got := RedactQuery("id=1+UNION+SELECT&x=secret")
	if strings.Contains(got, "UNION") || strings.Contains(got, "secret") {
		t.Fatalf("query_redacted still has secrets: %s", got)
	}
	if !strings.Contains(got, "id=") {
		t.Fatalf("should keep names: %s", got)
	}
}

func TestRedactSecretsStripsBearer(t *testing.T) {
	got := RedactSecrets("token=Bearer abcdef012345 and sk-secretkey")
	if strings.Contains(got, "abcdef") || strings.Contains(got, "sk-secret") {
		t.Fatalf("still has secret: %s", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("got %s", got)
	}
}

func TestPseudonymStable(t *testing.T) {
	a := Pseudonym([]byte("k"), "1.2.3.4")
	b := Pseudonym([]byte("k"), "1.2.3.4")
	if a != b || a == "" {
		t.Fatalf("a=%s b=%s", a, b)
	}
}
