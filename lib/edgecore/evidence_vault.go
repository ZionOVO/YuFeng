package edgecore

import (
	"bufio"
	"container/heap"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

type vaultRecord struct {
	Version   int       `json:"version"`
	Handle    string    `json:"handle"`
	Digest    string    `json:"digest"`
	Nonce     string    `json:"nonce"`
	Cipher    string    `json:"cipher"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	RiskScore float64   `json:"riskScore"`
}

type vaultLocation struct {
	path   string
	offset int64
	length int
	record vaultRecord
}

type vaultExpiry struct {
	handle    string
	path      string
	expiresAt time.Time
}

type vaultExpiryHeap []vaultExpiry

func (h vaultExpiryHeap) Len() int { return len(h) }

func (h vaultExpiryHeap) Less(i, j int) bool {
	if h[i].expiresAt.Equal(h[j].expiresAt) {
		return h[i].handle < h[j].handle
	}
	return h[i].expiresAt.Before(h[j].expiresAt)
}

func (h vaultExpiryHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *vaultExpiryHeap) Push(value any) { *h = append(*h, value.(vaultExpiry)) }

func (h *vaultExpiryHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

var (
	// ErrEvidenceVaultCapacity 表示硬容量已经耗尽，任何新证据都必须停止采集。
	ErrEvidenceVaultCapacity = errors.New("evidence vault capacity exceeded")
	// ErrEvidenceVaultLowRiskCapacity 表示低风险证据先于高风险保留空间停止采集。
	ErrEvidenceVaultLowRiskCapacity = errors.New("evidence vault low risk capacity exceeded")
)

// Configure 把证据库容量、单条上限和有效期收窄到已验签策略。
func (v *EvidenceVault) Configure(policy *artifactv1.TrafficReviewPolicy) error {
	if err := ValidateTrafficReviewPolicy(policy); err != nil {
		return err
	}
	if policy.GetMode() == artifactv1.TrafficReviewMode_TRAFFIC_REVIEW_MODE_OFF {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.maxBytes = policy.GetVaultMaxBytes()
	v.ttl = time.Duration(policy.GetEvidenceTtlSeconds()) * time.Second
	v.maxEntry = int(policy.GetMaxEvidenceBytes())
	return nil
}

// EvidenceVault 是边缘本地加密、追加、有界的候选证据库。
type EvidenceVault struct {
	mu             sync.Mutex
	dir            string
	aead           cipher.AEAD
	maxBytes       int64
	ttl            time.Duration
	maxEntry       int
	usedBytes      int64
	index          map[string]vaultLocation
	segmentSizes   map[string]int64
	segmentEntries map[string]int
	expirations    vaultExpiryHeap
}

// NewEvidenceVault 构造证据库；key 必须是独立的 32 字节密钥。
func NewEvidenceVault(dir string, key []byte) (*EvidenceVault, error) {
	if len(key) != 32 {
		return nil, errors.New("evidence vault key must be 32 bytes")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("evidence vault directory permissions are too broad")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	vault := &EvidenceVault{
		dir: dir, aead: aead, maxBytes: kernel.TrafficReviewVaultBytes,
		ttl: kernel.TrafficReviewEvidenceTTL, maxEntry: kernel.TrafficReviewEvidenceBytes,
		index: make(map[string]vaultLocation), segmentSizes: make(map[string]int64), segmentEntries: make(map[string]int),
	}
	if err := vault.rebuildIndexLocked(); err != nil {
		return nil, err
	}
	if err := vault.gcLocked(time.Now()); err != nil {
		return nil, err
	}
	return vault, nil
}

// Put 加密并固定一条证据；容量不足时拒绝新证据，不逐出未过期记录。
func (v *EvidenceVault) Put(raw []byte, now time.Time) (string, string, time.Time, error) {
	return v.PutRisk(raw, 100, now)
}

// PutRisk 按风险分层固定证据；低于六十分的证据不能消费最后百分之二十容量。
func (v *EvidenceVault) PutRisk(raw []byte, riskScore float64, now time.Time) (string, string, time.Time, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(raw) == 0 {
		return "", "", time.Time{}, errors.New("evidence is empty")
	}
	if len(raw) > v.maxEntry {
		raw = raw[:v.maxEntry]
	}
	var handle string
	for {
		var err error
		handle, err = randomVaultHandle()
		if err != nil {
			return "", "", time.Time{}, err
		}
		if _, exists := v.index[handle]; !exists {
			break
		}
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", time.Time{}, err
	}
	digestBytes := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(digestBytes[:])
	expires := now.Add(v.ttl).UTC()
	record := vaultRecord{Version: 2, Handle: handle, Digest: digest,
		Nonce: base64.RawStdEncoding.EncodeToString(nonce), CreatedAt: now.UTC(), ExpiresAt: expires, RiskScore: riskScore}
	ciphertext := v.aead.Seal(nil, nonce, raw, vaultRecordAAD(record))
	record.Cipher = base64.RawStdEncoding.EncodeToString(ciphertext)
	line, err := json.Marshal(record)
	if err != nil {
		return "", "", time.Time{}, err
	}
	line = append(line, '\n')
	projected := v.usedBytes + int64(len(line))
	if projected > v.maxBytes*4/5 {
		if err := v.gcLocked(now); err != nil {
			return "", "", time.Time{}, err
		}
		projected = v.usedBytes + int64(len(line))
	}
	if riskScore < 60 && projected > v.maxBytes*4/5 {
		return "", "", time.Time{}, ErrEvidenceVaultLowRiskCapacity
	}
	if projected > v.maxBytes {
		return "", "", time.Time{}, ErrEvidenceVaultCapacity
	}
	path := filepath.Join(v.dir, fmt.Sprintf("evidence-%d.jsonl", now.UTC().Truncate(time.Hour).Unix()))
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", time.Time{}, err
	}
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		_ = f.Close()
		return "", "", time.Time{}, err
	}
	written, writeErr := f.Write(line)
	if writeErr == nil && written != len(line) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		rollbackErr := f.Truncate(offset)
		return "", "", time.Time{}, errors.Join(writeErr, rollbackErr, f.Close())
	}
	if err := f.Sync(); err != nil {
		rollbackErr := f.Truncate(offset)
		return "", "", time.Time{}, errors.Join(err, rollbackErr, f.Close())
	}
	if err := f.Close(); err != nil {
		return "", "", time.Time{}, err
	}
	if created {
		if err := syncDirectory(v.dir); err != nil {
			return "", "", time.Time{}, err
		}
	}
	v.index[handle] = vaultLocation{path: path, offset: offset, length: len(line) - 1, record: record}
	v.usedBytes += int64(len(line))
	v.segmentSizes[path] += int64(len(line))
	v.segmentEntries[path]++
	heap.Push(&v.expirations, vaultExpiry{handle: handle, path: path, expiresAt: expires})
	return handle, digest, expires, nil
}

// Get 解密未过期证据，并校验摘要与认证附加数据。
func (v *EvidenceVault) Get(handle string, now time.Time) ([]byte, string, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	location, ok := v.index[handle]
	if !ok {
		return nil, "", false, nil
	}
	if !now.Before(location.record.ExpiresAt) {
		digest := location.record.Digest
		if err := v.gcLocked(now); err != nil {
			return nil, "", false, err
		}
		return nil, digest, false, nil
	}
	f, err := os.Open(location.path)
	if err != nil {
		return nil, "", false, err
	}
	line := make([]byte, location.length)
	_, readErr := f.ReadAt(line, location.offset)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return nil, "", false, err
	}
	var record vaultRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return nil, "", false, err
	}
	if record.Handle != handle || record.Digest != location.record.Digest || !record.ExpiresAt.Equal(location.record.ExpiresAt) {
		return nil, "", false, errors.New("evidence vault index mismatch")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(record.Nonce)
	if err != nil {
		return nil, "", false, err
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(record.Cipher)
	if err != nil {
		return nil, "", false, err
	}
	aad := []byte(record.Handle + "\x00" + record.Digest)
	if record.Version >= 2 {
		aad = vaultRecordAAD(record)
	}
	raw, err := v.aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, "", false, err
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != record.Digest {
		return nil, "", false, errors.New("evidence digest mismatch")
	}
	return raw, record.Digest, true, nil
}

// GC 删除已经过期且不再可领取的证据分段；调用方至少每分钟执行一次。
func (v *EvidenceVault) GC(now time.Time) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.gcLocked(now)
}

func (v *EvidenceVault) gcLocked(now time.Time) error {
	for v.expirations.Len() > 0 && !now.Before(v.expirations[0].expiresAt) {
		expired := heap.Pop(&v.expirations).(vaultExpiry)
		location, ok := v.index[expired.handle]
		if !ok || location.path != expired.path || !location.record.ExpiresAt.Equal(expired.expiresAt) {
			continue
		}
		delete(v.index, expired.handle)
		v.segmentEntries[expired.path]--
	}
	for path, entries := range v.segmentEntries {
		if entries != 0 {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		v.usedBytes -= v.segmentSizes[path]
		delete(v.segmentSizes, path)
		delete(v.segmentEntries, path)
	}
	return nil
}

func (v *EvidenceVault) rebuildIndexLocked() error {
	files, err := v.filesLocked()
	if err != nil {
		return err
	}
	for _, path := range files {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			return errors.Join(err, f.Close())
		}
		if info.Mode().Perm()&0o077 != 0 {
			return errors.Join(errors.New("evidence vault segment permissions are too broad"), f.Close())
		}
		reader := bufio.NewReaderSize(f, 16*1024)
		var offset int64
		var segmentHandles []string
		corrupt := false
		for {
			line, readErr := reader.ReadBytes('\n')
			if errors.Is(readErr, io.EOF) {
				if len(line) > 0 {
					if err := truncateVaultTail(f, path, offset); err != nil {
						return err
					}
				}
				break
			}
			if readErr != nil {
				_ = f.Close()
				return readErr
			}
			line = line[:len(line)-1]
			length := len(line)
			if length == 0 {
				offset++
				continue
			}
			var record vaultRecord
			if err := json.Unmarshal(line, &record); err != nil {
				corrupt = true
				break
			}
			if (record.Version != 1 && record.Version != 2) || record.Handle == "" || record.Digest == "" || record.Nonce == "" || record.Cipher == "" ||
				record.ExpiresAt.Before(record.CreatedAt) {
				corrupt = true
				break
			}
			if _, exists := v.index[record.Handle]; exists {
				corrupt = true
				break
			}
			v.index[record.Handle] = vaultLocation{path: path, offset: offset, length: length, record: record}
			segmentHandles = append(segmentHandles, record.Handle)
			v.segmentEntries[path]++
			heap.Push(&v.expirations, vaultExpiry{handle: record.Handle, path: path, expiresAt: record.ExpiresAt})
			offset += int64(length + 1)
		}
		if err := f.Close(); err != nil {
			return err
		}
		if corrupt {
			for _, handle := range segmentHandles {
				delete(v.index, handle)
			}
			delete(v.segmentEntries, path)
			target := strings.TrimSuffix(path, ".jsonl") + ".corrupt"
			if err := os.Rename(path, target); err != nil {
				return fmt.Errorf("quarantine evidence vault segment %s: %w", filepath.Base(path), err)
			}
			if err := syncDirectory(v.dir); err != nil {
				return err
			}
			continue
		}
		v.segmentSizes[path] = offset
		v.usedBytes += offset
	}
	return nil
}

func vaultRecordAAD(record vaultRecord) []byte {
	raw, _ := json.Marshal(struct {
		Version   int       `json:"version"`
		Handle    string    `json:"handle"`
		Digest    string    `json:"digest"`
		CreatedAt time.Time `json:"createdAt"`
		ExpiresAt time.Time `json:"expiresAt"`
		RiskScore float64   `json:"riskScore"`
	}{record.Version, record.Handle, record.Digest, record.CreatedAt, record.ExpiresAt, record.RiskScore})
	return raw
}

func truncateVaultTail(file *os.File, path string, offset int64) error {
	if err := file.Truncate(offset); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func (v *EvidenceVault) filesLocked() ([]string, error) {
	entries, err := os.ReadDir(v.dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "evidence-") && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, filepath.Join(v.dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

func randomVaultHandle() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	return "evh-" + hex.EncodeToString(value[:]), nil
}
