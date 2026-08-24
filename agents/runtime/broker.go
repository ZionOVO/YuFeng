package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"

	modelv1 "yufeng/proto/gen/modelv1"
)

type wireMsg struct {
	Op      string        `json:"op"`
	Nonce   string        `json:"nonce,omitempty"`
	EnvKeys []string      `json:"env_keys,omitempty"`
	Tool    string        `json:"tool,omitempty"`
	Args    string        `json:"args,omitempty"`
	Kind    string        `json:"kind,omitempty"`
	Payload string        `json:"payload,omitempty"`
	OK      bool          `json:"ok,omitempty"`
	Error   string        `json:"error,omitempty"`
	Result  string        `json:"result,omitempty"`
	Saga    *SagaProgress `json:"saga,omitempty"`
}

type brokerHub struct {
	conn  net.Conn
	nonce string
	cfg   *SuperviseConfig

	mu              sync.RWMutex
	keys            []string
	capabilityToken string
	terminalMu      sync.Mutex
	terminalKind    string
	terminalPayload string
	sagaMu          sync.RWMutex
	saga            SagaSnapshot
}

func newNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (h *brokerHub) serve() {
	if h == nil || h.conn == nil {
		return
	}
	defer h.conn.Close() //nolint:errcheck // 服务循环退出后仅做本地套接字尽力清理。
	dec := json.NewDecoder(h.conn)
	enc := json.NewEncoder(h.conn)
	for {
		var msg wireMsg
		if err := dec.Decode(&msg); err != nil {
			return
		}
		reply := h.handle(msg)
		if err := enc.Encode(reply); err != nil {
			return
		}
	}
}

func (h *brokerHub) handle(msg wireMsg) wireMsg {
	if msg.Nonce != h.nonce {
		return wireMsg{Op: msg.Op + "_err", Error: "broker nonce mismatch"}
	}
	switch msg.Op {
	case "hello":
		if envHasSecretKey(msg.EnvKeys) {
			return wireMsg{Op: "hello_err", Error: "capability token must not enter run environment"}
		}
		h.mu.Lock()
		h.keys = append([]string(nil), msg.EnvKeys...)
		h.mu.Unlock()
		if err := h.appendAudit("hello", msg.Kind); err != nil {
			return wireMsg{Op: "hello_err", Error: err.Error()}
		}
		return wireMsg{Op: "hello_ok", OK: true}
	case "invoke":
		if h.cfg == nil || h.cfg.Tools == nil {
			return wireMsg{Op: "invoke_err", Error: "failed_precondition: supervisor has no tool caller"}
		}
		if err := h.appendAudit("tool", msg.Tool); err != nil {
			return wireMsg{Op: "invoke_err", Error: err.Error()}
		}
		result, err := h.cfg.Tools.Invoke(h.cfg.invokeCtx(), h.cfg.currentAccessToken(), h.capability(), msg.Tool, msg.Args)
		if err != nil {
			return wireMsg{Op: "invoke_err", Error: err.Error()}
		}
		return wireMsg{Op: "invoke_ok", OK: true, Result: result}
	case "generate":
		if h.cfg == nil || h.cfg.Models == nil {
			return wireMsg{Op: "generate_err", Error: "failed_precondition: supervisor has no model caller"}
		}
		var request modelv1.GenerateRequest
		if err := protojson.Unmarshal([]byte(msg.Payload), &request); err != nil {
			return wireMsg{Op: "generate_err", Error: "invalid model request"}
		}
		response, err := h.cfg.Models.Generate(h.cfg.invokeCtx(), h.cfg.currentAccessToken(), h.capability(), &request)
		if err != nil {
			return wireMsg{Op: "generate_err", Error: err.Error()}
		}
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(response)
		if err != nil {
			return wireMsg{Op: "generate_err", Error: err.Error()}
		}
		return wireMsg{Op: "generate_ok", OK: true, Result: string(raw)}
	case "input":
		if h.cfg == nil {
			return wireMsg{Op: "input_err", Error: "failed_precondition: supervisor has no work input"}
		}
		raw, err := json.Marshal(h.cfg.Input)
		if err != nil {
			return wireMsg{Op: "input_err", Error: err.Error()}
		}
		return wireMsg{Op: "input_ok", OK: true, Result: string(raw)}
	case "audit":
		if err := h.appendAudit(msg.Kind, msg.Payload); err != nil {
			return wireMsg{Op: "audit_err", Error: err.Error()}
		}
		return wireMsg{Op: "audit_ok", OK: true}
	case "saga":
		if h.cfg == nil || h.cfg.Client == nil || msg.Saga == nil {
			return wireMsg{Op: "saga_err", Error: "failed_precondition: supervisor has no saga journal"}
		}
		if msg.Saga.Plan != nil {
			h.sagaMu.RLock()
			matches := h.saga.PlanDigest != "" && h.saga.PlanDigest == msg.Saga.Plan.PlanDigest
			h.sagaMu.RUnlock()
			if matches {
				return h.sagaReply("saga_ok")
			}
		}
		snapshot, err := h.cfg.Client.Saga(h.cfg.invokeCtx(), h.cfg.WorkID, h.cfg.LeaseID, h.cfg.LeaseEpoch, *msg.Saga)
		if err != nil {
			return wireMsg{Op: "saga_err", Error: err.Error()}
		}
		h.setSaga(snapshot)
		return h.sagaReply("saga_ok")
	case "extend":
		if h.cfg == nil || h.cfg.Client == nil {
			return wireMsg{Op: "extend_err", Error: "failed_precondition: supervisor has no work client"}
		}
		extension, err := h.cfg.Client.Extend(h.cfg.invokeCtx(), h.cfg.WorkID, h.cfg.LeaseID, h.cfg.LeaseEpoch)
		if err != nil {
			return wireMsg{Op: "extend_err", Error: err.Error()}
		}
		h.setCapability(extension.CapabilityToken)
		if err := h.appendAudit("extend", ""); err != nil {
			return wireMsg{Op: "extend_err", Error: err.Error()}
		}
		return wireMsg{Op: "extend_ok", OK: true}
	case "done":
		if err := h.acceptTerminal("done", msg.Payload); err != nil {
			return wireMsg{Op: "done_err", Error: err.Error()}
		}
		return wireMsg{Op: "done_ok", OK: true}
	case "fail":
		if err := h.acceptTerminal("fail", msg.Payload); err != nil {
			return wireMsg{Op: "fail_err", Error: err.Error()}
		}
		return wireMsg{Op: "fail_ok", OK: true}
	default:
		return wireMsg{Op: "err", Error: "unknown broker op"}
	}
}

func (h *brokerHub) appendAudit(kind, payload string) error {
	if h.cfg != nil && h.cfg.Client != nil && h.cfg.WorkID != "" {
		if err := h.cfg.Client.Progress(h.cfg.invokeCtx(), h.cfg.WorkID, h.cfg.LeaseID, h.cfg.LeaseEpoch, kind, payload); err != nil {
			return fmt.Errorf("report progress: %w", err)
		}
	}
	return nil
}

func (h *brokerHub) envKeys() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.keys...)
}

func (h *brokerHub) capability() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.capabilityToken
}

func (h *brokerHub) setCapability(token string) {
	h.mu.Lock()
	h.capabilityToken = token
	h.mu.Unlock()
}

func (h *brokerHub) setSaga(snapshot SagaSnapshot) {
	h.sagaMu.Lock()
	h.saga = snapshot
	h.sagaMu.Unlock()
}

func (h *brokerHub) sagaReply(op string) wireMsg {
	h.sagaMu.RLock()
	raw, err := json.Marshal(h.saga)
	h.sagaMu.RUnlock()
	if err != nil {
		return wireMsg{Op: "saga_err", Error: err.Error()}
	}
	return wireMsg{Op: op, OK: true, Result: string(raw)}
}

func (h *brokerHub) acceptTerminal(kind, payload string) error {
	h.terminalMu.Lock()
	defer h.terminalMu.Unlock()
	if h.terminalKind != "" {
		return fmt.Errorf("failed_precondition: run terminal receipt already recorded")
	}
	if err := h.appendAudit(kind, payload); err != nil {
		return err
	}
	h.terminalKind = kind
	h.terminalPayload = payload
	return nil
}

func (h *brokerHub) terminal() (string, string) {
	h.terminalMu.Lock()
	defer h.terminalMu.Unlock()
	return h.terminalKind, h.terminalPayload
}

// BrokerClient 是短命执行实例使用的本地监督代理客户端。
type BrokerClient struct {
	conn  net.Conn
	nonce string
	mu    sync.Mutex
	enc   *json.Encoder
	dec   *json.Decoder
}

// DialBroker 接管已连接的监督代理描述符。
func DialBroker(fd int, nonce string) (*BrokerClient, error) {
	return dialBrokerClient(fd, nonce)
}

func failedBroker() error {
	return fmt.Errorf("failed_precondition: local supervisor broker is required")
}

func jsonEncoder(conn net.Conn) *json.Encoder { return json.NewEncoder(conn) }
func jsonDecoder(conn net.Conn) *json.Decoder { return json.NewDecoder(conn) }

// Close 关闭代理连接。
func (c *BrokerClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Hello 证明执行实例持有监督代理随机数，并声明允许继承的环境变量名。
func (c *BrokerClient) Hello(keys []string) error {
	reply, err := c.roundTrip(wireMsg{Op: "hello", Nonce: c.nonce, EnvKeys: keys})
	if err != nil {
		return err
	}
	if !reply.OK {
		return fmt.Errorf("%s", firstNonEmpty(reply.Error, "broker hello rejected"))
	}
	return nil
}

// Invoke 经本地监督代理调用已授权工具，执行实例不会接触远端访问令牌。
func (c *BrokerClient) Invoke(tool, args string) (string, error) {
	reply, err := c.roundTrip(wireMsg{Op: "invoke", Nonce: c.nonce, Tool: tool, Args: args})
	if err != nil {
		return "", err
	}
	if !reply.OK {
		return "", fmt.Errorf("%s", firstNonEmpty(reply.Error, "broker invoke failed"))
	}
	return reply.Result, nil
}

// Generate 经本地监督代理调用统一模型网关，执行实例不接触凭据。
func (c *BrokerClient) Generate(request *modelv1.GenerateRequest) (*modelv1.GenerateResponse, error) {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(request)
	if err != nil {
		return nil, err
	}
	reply, err := c.roundTrip(wireMsg{Op: "generate", Nonce: c.nonce, Payload: string(raw)})
	if err != nil {
		return nil, err
	}
	if !reply.OK {
		return nil, fmt.Errorf("%s", firstNonEmpty(reply.Error, "broker generate failed"))
	}
	var response modelv1.GenerateResponse
	if err := protojson.Unmarshal([]byte(reply.Result), &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// Input 读取由当前工作项钉死的本地类型化输入。
func (c *BrokerClient) Input() (WorkInput, error) {
	reply, err := c.roundTrip(wireMsg{Op: "input", Nonce: c.nonce})
	if err != nil {
		return WorkInput{}, err
	}
	if !reply.OK {
		return WorkInput{}, fmt.Errorf("%s", firstNonEmpty(reply.Error, "broker input failed"))
	}
	var input WorkInput
	if err := json.Unmarshal([]byte(reply.Result), &input); err != nil {
		return WorkInput{}, fmt.Errorf("decode work input: %w", err)
	}
	if err := input.Validate(); err != nil {
		return WorkInput{}, err
	}
	return input, nil
}

// Audit 把执行实例产生的审计事实追加到服务端权威账本。
func (c *BrokerClient) Audit(kind, payload string) error {
	reply, err := c.roundTrip(wireMsg{Op: "audit", Nonce: c.nonce, Kind: kind, Payload: payload})
	return brokerReplyError(reply, err, "broker audit failed")
}

// BindSaga 在任何动作前固定计划并返回权威恢复快照。
func (c *BrokerClient) BindSaga(plan SagaPlan) (SagaSnapshot, error) {
	return c.exchangeSaga(SagaProgress{Plan: &plan})
}

// RecordSaga 在动作或补偿边界同步提交回执并返回新快照。
func (c *BrokerClient) RecordSaga(receipt SagaReceipt) (SagaSnapshot, error) {
	return c.exchangeSaga(SagaProgress{Receipt: &receipt})
}

func (c *BrokerClient) exchangeSaga(progress SagaProgress) (SagaSnapshot, error) {
	reply, err := c.roundTrip(wireMsg{Op: "saga", Nonce: c.nonce, Saga: &progress})
	return decodeSagaReply(reply, err)
}

func decodeSagaReply(reply wireMsg, err error) (SagaSnapshot, error) {
	if err != nil {
		return SagaSnapshot{}, err
	}
	if !reply.OK {
		return SagaSnapshot{}, fmt.Errorf("%s", firstNonEmpty(reply.Error, "broker saga request failed"))
	}
	var snapshot SagaSnapshot
	if err := json.Unmarshal([]byte(reply.Result), &snapshot); err != nil {
		return SagaSnapshot{}, fmt.Errorf("decode saga snapshot: %w", err)
	}
	return snapshot, nil
}

// Extend 请求延长当前工作租约，并保持工作执行权不变。
func (c *BrokerClient) Extend() error {
	reply, err := c.roundTrip(wireMsg{Op: "extend", Nonce: c.nonce})
	if err != nil {
		return err
	}
	if !reply.OK {
		return fmt.Errorf("%s", firstNonEmpty(reply.Error, "broker extend failed"))
	}
	return nil
}

// Done 提交工作成功终态及其结果引用。
func (c *BrokerClient) Done(payload string) error {
	reply, err := c.roundTrip(wireMsg{Op: "done", Nonce: c.nonce, Payload: payload})
	return brokerReplyError(reply, err, "broker completion failed")
}

// Fail 提交工作失败终态及其失败说明。
func (c *BrokerClient) Fail(payload string) error {
	reply, err := c.roundTrip(wireMsg{Op: "fail", Nonce: c.nonce, Payload: payload})
	return brokerReplyError(reply, err, "broker failure receipt failed")
}

func brokerReplyError(reply wireMsg, err error, fallback string) error {
	if err != nil {
		return err
	}
	if !reply.OK {
		return fmt.Errorf("%s", firstNonEmpty(reply.Error, fallback))
	}
	return nil
}

func (c *BrokerClient) roundTrip(msg wireMsg) (wireMsg, error) {
	var zero wireMsg
	if c == nil || c.conn == nil {
		return zero, fmt.Errorf("failed_precondition: local supervisor broker is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.enc.Encode(msg); err != nil {
		return zero, err
	}
	var reply wireMsg
	if err := c.dec.Decode(&reply); err != nil {
		if err == io.EOF {
			return zero, fmt.Errorf("supervisor broker closed")
		}
		return zero, err
	}
	return reply, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
