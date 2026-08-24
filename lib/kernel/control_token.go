package kernel

import (
	"errors"
	"io"
	"os"
	"strings"
	"unicode"
)

const (
	dataplaneControlTokenMinBytes = 32
	dataplaneControlTokenMaxBytes = 256
)

// LoadDataplaneControlToken 读取并校验数据面监督器单用途控制令牌文件。
func LoadDataplaneControlToken(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("dataplane control token file is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, dataplaneControlTokenMaxBytes+1))
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return "", err
	}
	if len(raw) > dataplaneControlTokenMaxBytes {
		return "", errors.New("dataplane control token is too long")
	}
	token := string(raw)
	if len(token) < dataplaneControlTokenMinBytes {
		return "", errors.New("dataplane control token is too short")
	}
	if strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return "", errors.New("dataplane control token contains whitespace")
	}
	for _, value := range []byte(token) {
		if value < 0x21 || value > 0x7e {
			return "", errors.New("dataplane control token contains invalid bytes")
		}
	}
	return token, nil
}
