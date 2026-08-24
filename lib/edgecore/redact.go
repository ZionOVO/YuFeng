package edgecore

import (
	"net/url"
	"strings"
)

// RedactQuery 去掉查询值，只保留参数名。生产 query_redacted 不得存原文。
func RedactQuery(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k+"=")
	}
	return strings.Join(names, "&")
}
