package brain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	evidencev1 "yufeng/proto/gen/evidencev1"
)

const sensitiveRelayMaxBytes = 64 << 20

type sensitiveRelayEntry struct {
	approvalID string
	caseID     string
	fragments  []*evidencev1.EvidenceFragment
	bytes      int64
	expiresAt  time.Time
}

func sensitiveEntryDigest(fragments []*evidencev1.EvidenceFragment) string {
	hash := sha256.New()
	for _, fragment := range fragments {
		if fragment == nil {
			continue
		}
		_, _ = hash.Write([]byte(fragment.GetEvidenceHandle()))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(fragment.GetField()))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(fragment.GetContentDigest()))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// SensitiveRelay 是不落盘的短期敏感证据中继。
type SensitiveRelay struct {
	mu      sync.Mutex
	entries map[string]sensitiveRelayEntry
	bytes   int64
}

// NewSensitiveRelay 构造最大 64 MiB 的内存中继。
func NewSensitiveRelay() *SensitiveRelay {
	return &SensitiveRelay{entries: map[string]sensitiveRelayEntry{}}
}

func (r *SensitiveRelay) put(ref string, entry sensitiveRelayEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(time.Now())
	if strings.TrimSpace(ref) == "" {
		return errors.New("sensitive relay reference is empty")
	}
	if _, exists := r.entries[ref]; exists {
		return errors.New("sensitive relay reference already exists")
	}
	if entry.bytes <= 0 || entry.bytes > sensitiveRelayMaxBytes || r.bytes+entry.bytes > sensitiveRelayMaxBytes {
		return errors.New("sensitive relay capacity exceeded")
	}
	r.entries[ref] = cloneSensitiveRelayEntry(entry)
	r.bytes += entry.bytes
	return nil
}

func (r *SensitiveRelay) get(ref string) (sensitiveRelayEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(time.Now())
	entry, ok := r.entries[ref]
	return cloneSensitiveRelayEntry(entry), ok
}

func cloneSensitiveRelayEntry(entry sensitiveRelayEntry) sensitiveRelayEntry {
	cloned := entry
	cloned.fragments = make([]*evidencev1.EvidenceFragment, 0, len(entry.fragments))
	for _, fragment := range entry.fragments {
		if fragment == nil {
			cloned.fragments = append(cloned.fragments, nil)
			continue
		}
		cloned.fragments = append(cloned.fragments, proto.Clone(fragment).(*evidencev1.EvidenceFragment))
	}
	return cloned
}

func (r *SensitiveRelay) consume(ref string) (sensitiveRelayEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(time.Now())
	entry, ok := r.entries[ref]
	if ok {
		delete(r.entries, ref)
		r.bytes -= entry.bytes
	}
	return entry, ok
}

func (r *SensitiveRelay) expireLocked(now time.Time) {
	for ref, entry := range r.entries {
		if !entry.expiresAt.After(now) {
			delete(r.entries, ref)
			r.bytes -= entry.bytes
		}
	}
}
