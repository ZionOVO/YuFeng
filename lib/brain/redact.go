package brain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"

	"yufeng/lib/edgecore"
)

var secretPat = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._\-+=/]+|sk-[a-z0-9]+|yufeng_[a-z0-9]+)`)

// RedactQuery 去掉查询值，只保留参数名。生产 query_redacted 不得存原文。
func RedactQuery(raw string) string {
	return edgecore.RedactQuery(raw)
}

// RedactSecrets 在进入审计与会话落盘前去掉令牌/密钥明文。
func RedactSecrets(s string) string {
	return secretPat.ReplaceAllString(s, "[redacted]")
}

// Pseudonym 使用基于哈希的消息认证码与安全哈希算法 256 位摘要生成稳定假名。
func Pseudonym(key []byte, raw string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}
