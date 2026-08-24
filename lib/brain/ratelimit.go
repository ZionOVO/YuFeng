package brain

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
)

// ClampPageSize 把列表页大小收进协议默认值与上限。
func ClampPageSize(n int32) int {
	if n <= 0 {
		return kernel.PageSizeDefault
	}
	if n > kernel.PageSizeMax {
		return kernel.PageSizeMax
	}
	return int(n)
}

// ClampArtifactBytes 收进 ListReleases / ListGenerations 字节预算。
func ClampArtifactBytes(n int32) int {
	if n <= 0 {
		return kernel.ArtifactPageMaxBytes
	}
	if n > kernel.ArtifactPageHardMaxBytes {
		return kernel.ArtifactPageHardMaxBytes
	}
	return int(n)
}

// UnitLimiter 是单元域合计每秒查询数限制，心跳不计入。
type UnitLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	lastSweep time.Time
}

const limiterKeyLimit = 10_000

func newUnitLimiter() *UnitLimiter {
	return &UnitLimiter{hits: map[string][]time.Time{}}
}

var defaultUnitLimiter = newUnitLimiter()

// AllowUnitRPC 在超限时返回 resource_exhausted。
func AllowUnitRPC(unitID string, now time.Time) error {
	if !defaultUnitLimiter.Allow(unitID, now) {
		return connect.NewError(connect.CodeResourceExhausted, errUnitQPS)
	}
	return nil
}

var errUnitQPS = errors.New("unit rpc qps exceeded")

// Allow 判定该单元本秒是否仍有配额。
func (l *UnitLimiter) Allow(unitID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-time.Second)
	if l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= time.Second {
		for key, timestamps := range l.hits {
			if len(timestamps) == 0 || !timestamps[len(timestamps)-1].After(cut) {
				delete(l.hits, key)
			}
		}
		l.lastSweep = now
	}
	if _, exists := l.hits[unitID]; !exists && len(l.hits) >= limiterKeyLimit {
		for key := range l.hits {
			delete(l.hits, key)
			break
		}
	}
	kept := l.hits[unitID][:0]
	for _, t := range l.hits[unitID] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= kernel.UnitRPCQPS {
		l.hits[unitID] = kept
		return false
	}
	l.hits[unitID] = append(kept, now)
	return true
}

type windowLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	limit     int
	window    time.Duration
	lastSweep time.Time
}

func newWindowLimiter(limit int, window time.Duration) *windowLimiter {
	return &windowLimiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

// Allow 判断指定键在当前滑动窗口内是否仍有调用配额。
func (l *windowLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-l.window)
	if l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= min(l.window, time.Second) {
		for candidate, timestamps := range l.hits {
			if len(timestamps) == 0 || !timestamps[len(timestamps)-1].After(cut) {
				delete(l.hits, candidate)
			}
		}
		l.lastSweep = now
	}
	if _, exists := l.hits[key]; !exists && len(l.hits) >= limiterKeyLimit {
		for candidate := range l.hits {
			delete(l.hits, candidate)
			break
		}
	}
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

func requestSource(peerAddress string, h http.Header, trustedProxies []netip.Prefix) string {
	direct := strings.TrimSpace(peerAddress)
	if host, _, err := net.SplitHostPort(direct); err == nil {
		direct = host
	}
	directAddress, directValid := netip.ParseAddr(strings.Trim(direct, "[]"))
	trusted := false
	if directValid == nil {
		for _, prefix := range trustedProxies {
			if prefix.Contains(directAddress) {
				trusted = true
				break
			}
		}
	}
	if trusted {
		if x := strings.TrimSpace(h.Get("X-Forwarded-For")); x != "" {
			if i := strings.IndexByte(x, ','); i >= 0 {
				x = x[:i]
			}
			if address, err := netip.ParseAddr(strings.TrimSpace(x)); err == nil {
				return address.String()
			}
		}
		if x := strings.TrimSpace(h.Get("X-Real-IP")); x != "" {
			if address, err := netip.ParseAddr(x); err == nil {
				return address.String()
			}
		}
	}
	if directValid == nil {
		return directAddress.String()
	}
	return "unknown"
}

func decodePageOffset(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 8 {
		return 0, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid page_token"))
	}
	n := binary.BigEndian.Uint64(raw)
	if n > uint64(^uint(0)>>1) {
		return 0, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid page_token"))
	}
	return int(n), nil
}

func encodePageOffset(n int) string {
	if n <= 0 {
		return ""
	}
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, uint64(n))
	return base64.RawURLEncoding.EncodeToString(raw)
}

type pollGate struct {
	mu sync.Mutex
	n  map[string]int
}

func newPollGate() *pollGate { return &pollGate{n: map[string]int{}} }

func (g *pollGate) acquire(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n[id] >= kernel.LongPollConcurrencyPerAgent {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("long poll concurrency exceeded"))
	}
	g.n[id]++
	return nil
}

func (g *pollGate) release(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n[id]--
	if g.n[id] <= 0 {
		delete(g.n, id)
	}
}
