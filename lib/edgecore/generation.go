package edgecore

import (
	"crypto/ed25519"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"yufeng/lib/kernel"
	"yufeng/lib/observability"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

// GenerationStore 保存当前与上一份已验证资产世代。
type GenerationStore struct {
	mu         sync.RWMutex
	current    *artifactv1.AssetGeneration
	prev       *artifactv1.AssetGeneration
	LoadErrors int
	diskFull   bool
	degraded   bool
}

// Load 验签并原子替换。部分损坏保留上一份；回滚授权必须写进已签名信封。
func (s *GenerationStore) Load(next *artifactv1.AssetGeneration, pub ed25519.PublicKey) error {
	if next == nil || next.AssetId == "" || next.GenerationSeq <= 0 {
		s.noteLoadError()
		return errors.New("generation envelope is incomplete")
	}
	for _, m := range next.Members {
		if m == nil || m.ReleaseId == "" || m.Artifact == nil {
			s.noteLoadError()
			return errors.New("generation member missing release_id")
		}
		if m.Artifact.Kind == artifactv1.Kind_KIND_LISTEN_PLAN {
			s.noteLoadError()
			return errors.New("unit listen plan must not be included in asset generation")
		}
		if err := kernel.VerifyArtifact(m.Artifact, pub); err != nil {
			s.noteLoadError()
			return err
		}
	}
	if err := kernel.VerifyGeneration(next, pub); err != nil {
		s.noteLoadError()
		return err
	}
	if nb := next.GetNotBefore(); nb != nil {
		until := time.Until(nb.AsTime())
		if until > kernel.ClockSkew {
			return errors.New("generation not_before not yet valid")
		}
	}
	s.mu.Lock()
	diskFull := s.diskFull
	s.mu.Unlock()
	if diskFull {
		s.mu.Lock()
		s.degraded = true
		s.mu.Unlock()
		return errors.New("disk full: refuse new generation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && next.AssetId != s.current.AssetId {
		return errors.New("generation asset must not change")
	}
	if s.current != nil && next.GenerationSeq == s.current.GenerationSeq {
		if next.GenerationId == s.current.GenerationId {
			return nil
		}
		return errors.New("generation sequence already has a different envelope")
	}
	if s.current != nil && next.GenerationSeq < s.current.GenerationSeq {
		if next.GetRollbackOf() != s.current.GenerationSeq {
			return errors.New("older generation requires signed rollback")
		}
		if next.GetParentGenerationId() != s.current.GenerationId {
			return errors.New("rollback generation parent does not match current envelope")
		}
	}
	if s.current != nil && next.GenerationSeq > s.current.GenerationSeq+1 {
		return errors.New("generation seq must not skip")
	}
	if s.current != nil && next.GenerationSeq == s.current.GenerationSeq+1 && next.ParentGenerationId != s.current.GenerationId {
		return errors.New("generation parent does not match current envelope")
	}
	if s.current != nil && next.GetRollbackOf() != 0 && next.GetRollbackOf() != s.current.GenerationSeq {
		return errors.New("rollback_of must identify current generation")
	}
	if s.current != nil {
		s.prev = s.current
	}
	s.current = proto.Clone(next).(*artifactv1.AssetGeneration)
	return nil
}

func (s *GenerationStore) noteLoadError() {
	s.mu.Lock()
	s.LoadErrors++
	s.mu.Unlock()
	observability.Default().Add(observability.MetricDetectorErrors, 1)
}

// SetDiskFull 磁盘满时拒绝新世代并标降级。
func (s *GenerationStore) SetDiskFull(full bool) {
	s.mu.Lock()
	s.diskFull = full
	if full {
		s.degraded = true
	}
	s.mu.Unlock()
}

// Degraded 报告磁盘满降级。
func (s *GenerationStore) Degraded() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.degraded
}

// Current 返回当前世代。
func (s *GenerationStore) Current() *artifactv1.AssetGeneration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return nil
	}
	return proto.Clone(s.current).(*artifactv1.AssetGeneration)
}
