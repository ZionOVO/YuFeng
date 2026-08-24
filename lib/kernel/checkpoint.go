package kernel

import (
	"bufio"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// AuditCheckpoint 是签名器验证并签发的审计账本链头。
type AuditCheckpoint struct {
	Sequence  int64     `json:"sequence"`
	Head      string    `json:"head"`
	CreatedAt time.Time `json:"created_at"`
	KeyID     string    `json:"key_id,omitempty"`
	Signature []byte    `json:"signature,omitempty"`
}

// WriteAuditCheckpoint 把审计哈希链头写到进程外只追加介质。
func WriteAuditCheckpoint(w io.Writer, seq int64, head string) error {
	if strings.TrimSpace(head) == "" {
		return fmt.Errorf("empty checkpoint hash")
	}
	_, err := fmt.Fprintf(w, "%d %s\n", seq, head)
	return err
}

// VerifyAuditCheckpoint 对照库内链头；不一致视为断裂。
func VerifyAuditCheckpoint(r io.Reader, seq int64, head string) error {
	sc := bufio.NewScanner(r)
	var lastSeq int64
	var lastHead string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("checkpoint record is invalid")
		}
		sequence, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return err
		}
		lastSeq, lastHead = sequence, fields[1]
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if lastHead == "" {
		return fmt.Errorf("checkpoint is empty")
	}
	if lastSeq != seq || lastHead != head {
		return fmt.Errorf("audit chain break: checkpoint %d %s vs db %d %s", lastSeq, lastHead, seq, head)
	}
	return nil
}

// SignAuditCheckpointWithSigner 通过类型化生产签名器签发账本链头。
func SignAuditCheckpointWithSigner(checkpoint *AuditCheckpoint, signer Signer) error {
	if checkpoint == nil || signer == nil {
		return fmt.Errorf("checkpoint and signer are required")
	}
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}
	raw, err := auditCheckpointEnvelope(checkpoint)
	if err != nil {
		return err
	}
	canonical, err := canonicalAuditCheckpoint(raw)
	if err != nil {
		return err
	}
	var signature []byte
	if typed, ok := signer.(typedSigner); ok {
		signature, err = typed.SignTyped(SignOperationAuditCheckpoint, raw)
	} else {
		signature, err = signer.Sign(canonical)
	}
	if err != nil {
		return err
	}
	checkpoint.KeyID = signer.KeyID()
	checkpoint.Signature = signature
	return nil
}

// WriteSignedAuditCheckpoint 追加一条带签名的检查点记录。
func WriteSignedAuditCheckpoint(w io.Writer, checkpoint *AuditCheckpoint) error {
	if checkpoint == nil || len(checkpoint.Signature) != ed25519.SignatureSize || checkpoint.KeyID == "" {
		return fmt.Errorf("signed checkpoint is invalid")
	}
	return json.NewEncoder(w).Encode(checkpoint)
}

// VerifySignedAuditCheckpoint 验证签名及期望链头。
func VerifySignedAuditCheckpoint(checkpoint *AuditCheckpoint, public ed25519.PublicKey, sequence int64, head string) error {
	if len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("signed checkpoint public key is invalid")
	}
	if checkpoint == nil || checkpoint.Sequence != sequence || checkpoint.Head != head || checkpoint.KeyID != KeyID(public) {
		return fmt.Errorf("signed checkpoint does not match ledger head")
	}
	raw, err := auditCheckpointEnvelope(checkpoint)
	if err != nil {
		return err
	}
	canonical, err := canonicalAuditCheckpoint(raw)
	if err != nil {
		return err
	}
	if !ed25519.Verify(public, canonical, checkpoint.Signature) {
		return fmt.Errorf("signed checkpoint signature is invalid")
	}
	return nil
}

func auditCheckpointEnvelope(checkpoint *AuditCheckpoint) ([]byte, error) {
	copy := *checkpoint
	copy.KeyID = ""
	copy.Signature = nil
	return json.Marshal(copy)
}

func canonicalAuditCheckpoint(raw []byte) ([]byte, error) {
	var checkpoint AuditCheckpoint
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return nil, fmt.Errorf("audit checkpoint signing envelope is invalid")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF || checkpoint.Sequence <= 0 || strings.TrimSpace(checkpoint.Head) == "" || checkpoint.CreatedAt.IsZero() || checkpoint.KeyID != "" || len(checkpoint.Signature) != 0 {
		return nil, fmt.Errorf("audit checkpoint signing envelope is invalid")
	}
	return json.Marshal(checkpoint)
}
