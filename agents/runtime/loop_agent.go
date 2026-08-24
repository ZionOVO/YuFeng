package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"connectrpc.com/connect"

	"yufeng/agents/modelgateway"

	agentv1 "yufeng/proto/gen/agentv1"
)

// InstructionClient 是指令轮询与确认。
type InstructionClient interface {
	Poll(ctx context.Context) ([]*agentv1.AgentInstruction, error)
	Extend(ctx context.Context, instructionID, leaseID string, leaseEpoch int64, capabilityToken string) (*agentv1.ExtendInstructionLeaseResponse, error)
	Ack(ctx context.Context, instructionID, leaseID string, leaseEpoch int64, status, errStr string) error
}

// AccessSession 持有可轮换的访问令牌。
type AccessSession struct {
	mu         sync.Mutex
	Access     string
	Refresh    string
	Renew      func(ctx context.Context, refresh string) (access, newRefresh string, err error)
	failure    chan struct{}
	failureErr error
}

// ErrAccessRefreshPersistence 标识服务端已轮换刷新令牌、但新令牌无法安全持久化。
//
// 该错误会永久关闭当前访问会话；调用方不得继续使用已经失效的旧刷新令牌。
var ErrAccessRefreshPersistence = errors.New("access refresh persistence failed")

// ErrAccessSessionFailed 标识访问会话已经永久关闭。
var ErrAccessSessionFailed = errors.New("access session failed")

// AccessToken 返回当前轮换代次的访问令牌。
func (s *AccessSession) AccessToken() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Access
}

// Tokens 返回当前访问与刷新令牌快照。
func (s *AccessSession) Tokens() (string, string) {
	if s == nil {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Access, s.Refresh
}

// SetTokens 原子更新当前访问与刷新令牌。
func (s *AccessSession) SetTokens(access, refresh string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.Access, s.Refresh = access, refresh
	s.mu.Unlock()
}

// Call 用当前访问令牌发起调用；未认证或过期时串行续期并只重试一次。
// 非认证错误不会触发续期，永久关闭的会话不会继续发起远端调用。
func (s *AccessSession) Call(ctx context.Context, fallback string, call func(access string) error) error {
	if call == nil {
		return errors.New("access session call is nil")
	}
	if s == nil {
		return call(fallback)
	}
	access, failure := s.accessOrFailure()
	if failure != nil {
		return failure
	}
	if err := call(access); err != nil {
		if !isAccessRejection(err) {
			return err
		}
		if err := s.refreshRejected(ctx, access); err != nil {
			return err
		}
		access, failure = s.accessOrFailure()
		if failure != nil {
			return failure
		}
		return call(access)
	}
	return nil
}

// Failure 返回会话永久关闭时会被关闭的信号。
func (s *AccessSession) Failure() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = make(chan struct{})
	}
	return s.failure
}

// FailureErr 返回导致当前访问会话永久关闭的错误。
func (s *AccessSession) FailureErr() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failureErr
}

// FailRefreshPersistence 因刷新令牌持久化失败永久关闭当前访问会话。
func (s *AccessSession) FailRefreshPersistence(err error) error {
	if s == nil {
		return fmt.Errorf("%w: %w", ErrAccessSessionFailed, accessRefreshPersistenceError(err))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failLocked(accessRefreshPersistenceError(err))
}

// RefreshIfUnauth 在未认证错误时用刷新令牌续期。
func (s *AccessSession) RefreshIfUnauth(ctx context.Context, err error) bool {
	if s == nil || err == nil || !isAccessRejection(err) {
		return false
	}
	access := s.AccessToken()
	return s.refreshRejected(ctx, access) == nil
}

func (s *AccessSession) refreshRejected(ctx context.Context, rejectedAccess string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failureErr != nil {
		return s.failureErr
	}
	// 同一代次的并发请求只允许一个请求续期；等待者直接复用已经轮换的代次。
	if s.Access != rejectedAccess {
		return nil
	}
	if s.Renew == nil {
		return errors.New("access session renewer is not configured")
	}
	access, refresh, err := s.Renew(ctx, s.Refresh)
	if err != nil {
		if errors.Is(err, ErrAccessRefreshPersistence) {
			return s.failLocked(err)
		}
		return err
	}
	if access == "" || refresh == "" {
		return errors.New("access session renewal returned empty tokens")
	}
	s.Access, s.Refresh = access, refresh
	return nil
}

func (s *AccessSession) accessOrFailure() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Access, s.failureErr
}

func (s *AccessSession) failLocked(err error) error {
	if s.failureErr != nil {
		return s.failureErr
	}
	if err == nil {
		err = ErrAccessRefreshPersistence
	}
	s.failureErr = fmt.Errorf("%w: %w", ErrAccessSessionFailed, err)
	if s.failure == nil {
		s.failure = make(chan struct{})
	}
	close(s.failure)
	return s.failureErr
}

func accessRefreshPersistenceError(err error) error {
	if err == nil {
		return ErrAccessRefreshPersistence
	}
	if errors.Is(err, ErrAccessRefreshPersistence) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrAccessRefreshPersistence, err)
}

func isAccessRejection(err error) bool {
	if err == nil {
		return false
	}
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		return true
	}
	return containsFold(err.Error(), "unauthenticated")
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || containsASCII(s, sub))
}

func containsASCII(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		ok := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// RunInstructions 是贾维斯主循环：领指令 → 模型 → 工具 → Ack。入口只装配本函数。
func RunInstructions(ctx context.Context, provider modelgateway.Provider, tools ToolCaller, client InstructionClient, sess *AccessSession) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ins, err := client.Poll(ctx)
		if err != nil {
			if sess != nil && sess.RefreshIfUnauth(ctx, err) {
				continue
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		access := ""
		if sess != nil {
			access = sess.AccessToken()
		}
		for _, item := range ins {
			if item == nil {
				continue
			}
			status, ackErr := "acked", ""
			if err := handleLeasedInstruction(ctx, provider, tools, client, item, access); err != nil {
				log.Printf("处理指令 %s 失败: %v", item.InstructionId, err)
				status, ackErr = "failed", err.Error()
			}
			if err := client.Ack(ctx, item.InstructionId, item.LeaseId, item.LeaseEpoch, status, ackErr); err != nil {
				log.Printf("确认指令失败: %v", err)
			}
		}
	}
}

type instructionLeaseState struct {
	mu         sync.RWMutex
	capability string
}

func (s *instructionLeaseState) get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.capability
}

func (s *instructionLeaseState) set(capability string) {
	s.mu.Lock()
	s.capability = capability
	s.mu.Unlock()
}

type leaseBoundTools struct {
	tools ToolCaller
	lease *instructionLeaseState
}

type leaseBoundProvider struct {
	provider modelgateway.Provider
	lease    *instructionLeaseState
}

// Complete 使用当前租约绑定的能力令牌完成一次模型生成。
func (p leaseBoundProvider) Complete(ctx context.Context, req modelgateway.ChatRequest) (modelgateway.ChatResponse, error) {
	if req.Turn != nil {
		turn := *req.Turn
		turn.CapabilityToken = p.lease.get()
		req.Turn = &turn
	}
	return p.provider.Complete(ctx, req)
}

// Invoke 忽略调用方提供的能力令牌，并强制使用当前租约持有的令牌调用工具。
func (t leaseBoundTools) Invoke(ctx context.Context, accessToken, _ string, name, argsJSON string) (string, error) {
	return t.tools.Invoke(ctx, accessToken, t.lease.get(), name, argsJSON)
}

func handleLeasedInstruction(ctx context.Context, provider modelgateway.Provider, tools ToolCaller, client InstructionClient, item *agentv1.AgentInstruction, access string) error {
	if item.GetLeaseDeadline() == nil || !item.GetLeaseDeadline().IsValid() {
		return fmt.Errorf("instruction lease deadline is required")
	}
	lease := &instructionLeaseState{capability: item.GetCapabilityToken()}
	if lease.capability == "" {
		return fmt.Errorf("instruction capability token is required")
	}
	handleCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Handle(handleCtx, leaseBoundProvider{provider: provider, lease: lease},
			leaseBoundTools{tools: tools, lease: lease}, item, access)
	}()
	deadline := item.GetLeaseDeadline().AsTime()
	for {
		wait, err := instructionRenewAfter(deadline)
		if err != nil {
			return err
		}
		timer := time.NewTimer(wait)
		select {
		case err := <-done:
			stopTimer(timer)
			return err
		case <-ctx.Done():
			stopTimer(timer)
			return ctx.Err()
		case <-timer.C:
			resp, err := client.Extend(ctx, item.GetInstructionId(), item.GetLeaseId(), item.GetLeaseEpoch(), lease.get())
			if err != nil {
				return fmt.Errorf("extend instruction lease: %w", err)
			}
			if err := validateInstructionExtension(item, resp); err != nil {
				return err
			}
			lease.set(resp.GetCapabilityToken())
			deadline = resp.GetLeaseDeadline().AsTime()
		}
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func instructionRenewAfter(deadline time.Time) (time.Duration, error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, fmt.Errorf("instruction lease expired")
	}
	wait := remaining / 2
	if wait <= 0 {
		return 0, fmt.Errorf("instruction lease deadline is too close")
	}
	return wait, nil
}

func validateInstructionExtension(item *agentv1.AgentInstruction, resp *agentv1.ExtendInstructionLeaseResponse) error {
	if resp == nil || resp.GetLeaseDeadline() == nil || !resp.GetLeaseDeadline().IsValid() || !resp.GetLeaseDeadline().AsTime().After(time.Now()) {
		return fmt.Errorf("instruction lease extension returned invalid deadline")
	}
	if resp.GetCapabilityToken() == "" {
		return fmt.Errorf("instruction lease extension returned empty capability token")
	}
	if resp.GetLeaseId() != item.GetLeaseId() || resp.GetLeaseEpoch() != item.GetLeaseEpoch() || resp.GetBudgetId() != item.GetBudgetId() {
		return fmt.Errorf("instruction lease extension changed ownership or budget")
	}
	return nil
}
