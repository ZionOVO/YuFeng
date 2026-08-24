package edgecore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"yufeng/lib/kernel"
)

const forwardedForHeader = "X-Forwarded-For"

// ClientSourceResolver 按签名可信代理策略解析客户端来源。
type ClientSourceResolver struct {
	trusted []netip.Prefix
}

// NewClientSourceResolver 编译规范的无类别域间路由前缀；空集表示只信直接对端。
func NewClientSourceResolver(cidrs []string) (*ClientSourceResolver, error) {
	normalized, err := kernel.NormalizeTrustedProxyCIDRs(cidrs)
	if err != nil {
		return nil, err
	}
	resolver := &ClientSourceResolver{trusted: make([]netip.Prefix, 0, len(normalized))}
	for _, raw := range normalized {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, errors.New("parse normalized trusted proxy cidr")
		}
		resolver.trusted = append(resolver.trusted, prefix)
	}
	return resolver, nil
}

// Resolve 仅在直接对端可信时解析 X-Forwarded-For；头界非法则回退直接对端。
func (r *ClientSourceResolver) Resolve(remoteAddress string, headers http.Header) netip.Addr {
	direct, ok := parsePeerAddress(remoteAddress)
	if !ok || r == nil || !r.contains(direct) {
		return direct
	}
	values := headers.Values(forwardedForHeader)
	if len(values) == 0 {
		return direct
	}
	chain := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			addr, err := netip.ParseAddr(part)
			if part == "" || err != nil || addr.Zone() != "" {
				return direct
			}
			chain = append(chain, addr.Unmap())
		}
	}
	if len(chain) == 0 {
		return direct
	}
	source := direct
	for i := len(chain) - 1; i >= 0; i-- {
		source = chain[i]
		if !r.contains(source) {
			return source
		}
	}
	return source
}

func (r *ClientSourceResolver) contains(addr netip.Addr) bool {
	if r == nil || !addr.IsValid() {
		return false
	}
	for _, prefix := range r.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func parsePeerAddress(raw string) (netip.Addr, bool) {
	raw = strings.TrimSpace(raw)
	if addr, err := netip.ParseAddr(raw); err == nil && addr.Zone() == "" {
		return addr.Unmap(), true
	}
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// SourcePseudonymizer 用部署作用域秘密生成不可逆的来源假名。
type SourcePseudonymizer struct {
	key   [sha256.Size]byte
	keyID string
}

// NewSourcePseudonymizer 只接受 256 位随机密钥。
func NewSourcePseudonymizer(key []byte) (SourcePseudonymizer, error) {
	if len(key) != sha256.Size {
		return SourcePseudonymizer{}, errors.New("source pseudonym key must be 32 bytes")
	}
	var out SourcePseudonymizer
	copy(out.key[:], key)
	id := sha256.Sum256(key)
	out.keyID = hex.EncodeToString(id[:8])
	return out, nil
}

// Pseudonym 对规范互联网协议地址字节计算带域分隔的基于哈希的消息认证码与安全哈希算法 256 位摘要。
func (p SourcePseudonymizer) Pseudonym(addr netip.Addr) string {
	if !addr.IsValid() || p.keyID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, p.key[:])
	_, _ = mac.Write([]byte("yufeng/client-source/v1\x00"))
	_, _ = mac.Write(addr.Unmap().AsSlice())
	return "h1." + p.keyID + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
