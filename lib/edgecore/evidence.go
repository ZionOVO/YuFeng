package edgecore

import (
	"sync"
	"time"

	"yufeng/lib/kernel"
)

type evidenceItem struct {
	raw []byte
	exp time.Time
}

// EvidenceRing 是边缘本地证据环缓冲，按架构预算存活 15 分钟，不跨边缘复制。
type EvidenceRing struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	maxBytes   int
	bytes      int
	order      []string
	items      map[string]evidenceItem
}

// NewEvidenceRing 构造环缓冲。
func NewEvidenceRing() *EvidenceRing {
	return &EvidenceRing{
		ttl:        kernel.EvidenceRingTTL,
		maxEntries: kernel.EvidenceRingMaxEntries,
		maxBytes:   kernel.EvidenceRingMaxBytes,
		items:      map[string]evidenceItem{},
	}
}

// Put 写入原文证据。先到先丢：条数或字节超上限时驱逐最旧项。
func (r *EvidenceRing) Put(id string, raw []byte, now time.Time) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gc(now)
	if old, ok := r.items[id]; ok {
		r.bytes -= len(old.raw)
		r.removeOrder(id)
	}
	cp := append([]byte(nil), raw...)
	r.items[id] = evidenceItem{raw: cp, exp: now.Add(r.ttl)}
	r.order = append(r.order, id)
	r.bytes += len(cp)
	for (r.maxEntries > 0 && len(r.items) > r.maxEntries) || (r.maxBytes > 0 && r.bytes > r.maxBytes) {
		if len(r.order) == 0 {
			break
		}
		oldest := r.order[0]
		r.order = r.order[1:]
		if it, ok := r.items[oldest]; ok {
			r.bytes -= len(it.raw)
			delete(r.items, oldest)
		}
	}
}

func (r *EvidenceRing) removeOrder(id string) {
	for i, k := range r.order {
		if k == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			return
		}
	}
}

// Get 读取未过期原文；过期或不存在返回 false。
func (r *EvidenceRing) Get(id string, now time.Time) ([]byte, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gc(now)
	it, ok := r.items[id]
	if !ok || !it.exp.After(now) {
		return nil, false
	}
	return append([]byte(nil), it.raw...), true
}

func (r *EvidenceRing) gc(now time.Time) {
	for k, it := range r.items {
		if !it.exp.After(now) {
			r.bytes -= len(it.raw)
			delete(r.items, k)
			r.removeOrder(k)
		}
	}
}
