package brain

import (
	"net/http"
	"net/netip"
	"testing"
	"time"
)

func TestRequestSourceTrustsForwardedHeadersOnlyFromConfiguredProxy(t *testing.T) {
	header := http.Header{"X-Forwarded-For": []string{"203.0.113.9"}}
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	if got := requestSource("198.51.100.4:443", header, trusted); got != "198.51.100.4" {
		t.Fatalf("untrusted direct peer source=%q", got)
	}
	if got := requestSource("10.1.2.3:443", header, trusted); got != "203.0.113.9" {
		t.Fatalf("trusted proxy source=%q", got)
	}
}

func TestWindowLimiterBoundsRandomSourceKeysAndExpiresThem(t *testing.T) {
	limiter := newWindowLimiter(2, time.Minute)
	now := time.Now()
	for index := 0; index < limiterKeyLimit+100; index++ {
		limiter.Allow(string(rune(index+1)), now)
	}
	if len(limiter.hits) > limiterKeyLimit {
		t.Fatalf("keys=%d exceed hard limit", len(limiter.hits))
	}
	limiter.Allow("fresh", now.Add(2*time.Minute))
	if len(limiter.hits) != 1 {
		t.Fatalf("expired keys retained: %d", len(limiter.hits))
	}
}
