package kernel

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

// SignOperation 是签名器允许处理的类型化操作闭集。
type SignOperation string

const (
	SignOperationArtifact        SignOperation = "artifact"
	SignOperationAssetGeneration SignOperation = "asset_generation"
	SignOperationListenPlan      SignOperation = "unit_listen_plan"
	SignOperationAuditCheckpoint SignOperation = "audit_checkpoint"
	signingProtocolVersion                     = "signer/v1"
	signingMessageLimit                        = 1 << 20
)

type typedSigner interface {
	SignTyped(SignOperation, []byte) ([]byte, error)
}

type signingRequest struct {
	Version   string        `json:"version"`
	Operation SignOperation `json:"operation"`
	Envelope  []byte        `json:"envelope"`
}

type signingResponse struct {
	Signature []byte `json:"signature,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Signer 是治理内核对外的签名接口。
// 生产构建不得从文件系统读取私钥；开发必须显式 -dev-insecure。
//
// [签名器]: ../../docs/glossary.md#signer
type Signer interface {
	// Sign 只接受已通过确定性校验、且已入账并过门禁的对象摘要。
	Sign(digest []byte) ([]byte, error)
	// Public 返回验签公钥。
	Public() ed25519.PublicKey
	// KeyID 返回公钥指纹。
	KeyID() string
}

// MemorySigner 用进程内私钥签名，仅测试与 -dev-insecure。
type MemorySigner struct {
	key          ed25519.PrivateKey
	RollbackOnly bool
}

// NewMemorySigner 构造内存签名器。
func NewMemorySigner(key ed25519.PrivateKey) (*MemorySigner, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid private key length")
	}
	return &MemorySigner{key: key}, nil
}

// Sign 对摘要做 Ed25519 签名。回滚钥不能签发新策略。
func (s *MemorySigner) Sign(digest []byte) ([]byte, error) {
	if s.RollbackOnly {
		return nil, errors.New("rollback key cannot create policy")
	}
	if len(digest) == 0 {
		return nil, errors.New("empty digest")
	}
	return ed25519.Sign(s.key, digest), nil
}

// Public 返回公钥。
func (s *MemorySigner) Public() ed25519.PublicKey {
	return s.key.Public().(ed25519.PublicKey)
}

// KeyID 返回公钥指纹。
func (s *MemorySigner) KeyID() string { return KeyID(s.Public()) }

// SocketSigner 经独立套接字请求签名，生产最小形态。
type SocketSigner struct {
	addr string
	pub  ed25519.PublicKey
}

// NewSocketSigner 构造套接字签名器。
func NewSocketSigner(addr string, pub ed25519.PublicKey) (*SocketSigner, error) {
	if addr == "" {
		return nil, errors.New("signer socket address is empty")
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("invalid public key length")
	}
	return &SocketSigner{addr: addr, pub: pub}, nil
}

// Sign 把摘要交给套接字对端签名。
func (s *SocketSigner) Sign(digest []byte) ([]byte, error) {
	return nil, errors.New("socket signer requires a typed signing operation")
}

// SignTyped 请求签名器解析类型化信封并重建规范签名字节。
func (s *SocketSigner) SignTyped(operation SignOperation, envelope []byte) ([]byte, error) {
	if len(envelope) == 0 || len(envelope) > signingMessageLimit {
		return nil, errors.New("typed signing envelope is empty or too large")
	}
	request := signingRequest{Version: signingProtocolVersion, Operation: operation, Envelope: envelope}
	canonical, err := canonicalTypedSigningRequest(request, s.pub)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", s.addr, time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close() //nolint:errcheck // 请求已完成后仅做短连接尽力清理。
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if err := writeSigningFrame(conn, raw); err != nil {
		return nil, err
	}
	responseRaw, err := readSigningFrame(conn)
	if err != nil {
		return nil, err
	}
	var response signingResponse
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	if len(response.Signature) != ed25519.SignatureSize {
		return nil, errors.New("signer returned an invalid signature")
	}
	if !ed25519.Verify(s.pub, canonical, response.Signature) {
		return nil, errors.New("signer returned a signature that does not verify")
	}
	return response.Signature, nil
}

// Public 返回公钥。
func (s *SocketSigner) Public() ed25519.PublicKey { return s.pub }

// KeyID 返回公钥指纹。
func (s *SocketSigner) KeyID() string { return KeyID(s.pub) }

// ServeMemorySigner 在 Unix 套接字上提供类型化签名，测试不限制对端用户。
func ServeMemorySigner(ln net.Listener, inner *MemorySigner) error {
	return ServeMemorySignerForUID(ln, inner, -1)
}

// ServeMemorySignerForUID 只接受指定本机用户发起的类型化签名请求。
func ServeMemorySignerForUID(ln net.Listener, inner *MemorySigner, allowedUID int) error {
	if ln == nil || inner == nil {
		return errors.New("signer listener and key are required")
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			defer c.Close() //nolint:errcheck // 签名应答结束后仅做短连接尽力清理。
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			if err := verifyUnixPeerUID(c, allowedUID); err != nil {
				return
			}
			raw, err := readSigningFrame(c)
			if err != nil {
				return
			}
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			var request signingRequest
			if err := decoder.Decode(&request); err != nil {
				_ = writeSigningResponse(c, signingResponse{Error: "invalid typed signing request"})
				return
			}
			var trailing json.RawMessage
			if err := decoder.Decode(&trailing); err != io.EOF {
				_ = writeSigningResponse(c, signingResponse{Error: "invalid typed signing request"})
				return
			}
			canonical, err := canonicalTypedSigningRequest(request, inner.Public())
			if err != nil {
				_ = writeSigningResponse(c, signingResponse{Error: err.Error()})
				return
			}
			sig, err := inner.Sign(canonical)
			if err != nil {
				_ = writeSigningResponse(c, signingResponse{Error: err.Error()})
				return
			}
			_ = writeSigningResponse(c, signingResponse{Signature: sig})
		}(conn)
	}
}

func canonicalTypedSigningRequest(request signingRequest, public ed25519.PublicKey) ([]byte, error) {
	if request.Version != signingProtocolVersion || len(request.Envelope) == 0 || len(request.Envelope) > signingMessageLimit {
		return nil, errors.New("signing protocol version or envelope is invalid")
	}
	switch request.Operation {
	case SignOperationArtifact:
		var artifact artifactv1.Artifact
		if err := proto.Unmarshal(request.Envelope, &artifact); err != nil || artifact.GetId() == "" || artifact.GetKind() == artifactv1.Kind_KIND_UNSPECIFIED || len(artifact.GetPayload()) == 0 || artifact.GetPayloadSchema() == "" || artifact.GetSignature() != nil {
			return nil, errors.New("artifact signing envelope is invalid")
		}
		want, err := ArtifactID(&artifact)
		if err != nil || artifact.GetId() != want {
			return nil, errors.New("artifact signing identity is invalid")
		}
		return canonicalBytes(&artifact)
	case SignOperationAssetGeneration:
		var generation artifactv1.AssetGeneration
		if err := proto.Unmarshal(request.Envelope, &generation); err != nil || generation.GetGenerationId() == "" || generation.GetAssetId() == "" || generation.GetGenerationSeq() <= 0 || generation.GetEnvelopeSignature() != nil {
			return nil, errors.New("asset generation signing envelope is invalid")
		}
		for _, member := range generation.GetMembers() {
			if member.GetArtifact() == nil || VerifyArtifact(member.GetArtifact(), public) != nil {
				return nil, errors.New("asset generation contains an unsigned member")
			}
		}
		return generationCanonical(&generation)
	case SignOperationListenPlan:
		var plan artifactv1.UnitListenPlan
		if err := proto.Unmarshal(request.Envelope, &plan); err != nil || plan.GetUnitId() == "" || plan.GetVersion() == 0 || plan.GetSignature() != nil {
			return nil, errors.New("unit listen plan signing envelope is invalid")
		}
		return unitListenPlanBytes(&plan)
	case SignOperationAuditCheckpoint:
		return canonicalAuditCheckpoint(request.Envelope)
	default:
		return nil, errors.New("signing operation is not allowed")
	}
}

func readSigningFrame(reader io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(header[:]))
	if size <= 0 || size > signingMessageLimit {
		return nil, errors.New("signing frame exceeds limit")
	}
	raw := make([]byte, size)
	_, err := io.ReadFull(reader, raw)
	return raw, err
}

func writeSigningFrame(writer io.Writer, raw []byte) error {
	if len(raw) == 0 || len(raw) > signingMessageLimit {
		return errors.New("signing frame exceeds limit")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err := writer.Write(raw)
	return err
}

func writeSigningResponse(writer io.Writer, response signingResponse) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return writeSigningFrame(writer, raw)
}

// ValidateSignerSource 拒绝生产配置指向私钥文件。
func ValidateSignerSource(devInsecure bool, keyFile string) error {
	if devInsecure {
		return nil
	}
	if keyFile != "" {
		if _, err := os.Stat(keyFile); err == nil {
			return errors.New("production signer must not read a private key file")
		}
	}
	return nil
}

// ValidateProductionSigner 生产必须走套接字/KMS，禁止文件私钥与静默内存钥。
func ValidateProductionSigner(devInsecure bool, keyFile, socket string) error {
	if devInsecure {
		return nil
	}
	if keyFile != "" {
		return errors.New("production signer must not read a private key file")
	}
	if socket == "" {
		return errors.New("production signer requires a socket endpoint")
	}
	return nil
}
