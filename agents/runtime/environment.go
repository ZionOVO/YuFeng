package runtime

import (
	"os"
	"strconv"
	"strings"
)

const (
	envWorkID        = "YUFENG_WORK_ID"
	envRunID         = "YUFENG_RUN_ID"
	envNonce         = "YUFENG_NONCE"
	envBrokerFD      = "YUFENG_BROKER_FD"
	envBrokerPipe    = "YUFENG_BROKER_PIPE"
	envSupervisorFD  = "YUFENG_SUPERVISOR_FD"
	envCancelFD      = "YUFENG_CANCEL_FD"
	envSupervisorPID = "YUFENG_SUPERVISOR_PID"
	envCancelEvent   = "YUFENG_CANCEL_EVENT"
	envMemoryLimit   = "YUFENG_MEMORY_LIMIT"
	envRlimitCPU     = "YUFENG_RLIMIT_CPU"
	envRlimitNOFILE  = "YUFENG_RLIMIT_NOFILE"
)

var secretEnvNames = map[string]struct{}{
	"YUFENG_CAPABILITY":      {},
	"YUFENG_ACCESS":          {},
	"YUFENG_REFRESH":         {},
	"YUFENG_BOOTSTRAP_TOKEN": {},
	"YUFENG_MODEL_API_KEY":   {},
}

// ChildEnv 是执行实例子进程允许继承的最小环境：工作项标识、一次性随机数、监督代理、存活与取消管道描述符。
// 不得包含访问令牌、刷新令牌、能力令牌或模型密钥。
func ChildEnv(workID, runID, nonce string, brokerFD, supervisorFD int, cancelFD ...int) []string {
	env := []string{
		envWorkID + "=" + workID,
		envRunID + "=" + runID,
		envNonce + "=" + nonce,
		envBrokerFD + "=" + strconv.Itoa(brokerFD),
		envSupervisorFD + "=" + strconv.Itoa(supervisorFD),
	}
	if len(cancelFD) > 0 && cancelFD[0] >= 3 {
		env = append(env, envCancelFD+"="+strconv.Itoa(cancelFD[0]))
	}
	for _, key := range []string{"PATH", "TMPDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// StripSecrets 去掉会把凭证带进执行实例的环境项。
func StripSecrets(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if isSecretEnv(envName(e)) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func envHasSecret(env []string) bool {
	for _, e := range env {
		if isSecretEnv(envName(e)) {
			return true
		}
	}
	return false
}

func envHasSecretKey(keys []string) bool {
	for _, k := range keys {
		if isSecretEnv(k) {
			return true
		}
	}
	return false
}

func isSecretEnv(name string) bool {
	_, ok := secretEnvNames[name]
	return ok
}

func envName(e string) string {
	name, _, _ := strings.Cut(e, "=")
	return name
}

func parseUintEnv(name string) uint64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// limitEnv 把资源上限写成非密钥环境项，供子进程 LimitResources 读取。
func limitEnv(lim ResourceLimit) []string {
	var out []string
	if lim.MemoryBytes > 0 {
		out = append(out, envMemoryLimit+"="+strconv.FormatUint(lim.MemoryBytes, 10))
	}
	if lim.CPUSeconds > 0 {
		out = append(out, envRlimitCPU+"="+strconv.FormatUint(lim.CPUSeconds, 10))
	}
	if lim.Files > 0 {
		out = append(out, envRlimitNOFILE+"="+strconv.FormatUint(lim.Files, 10))
	}
	return out
}

func envKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, e := range env {
		if name := envName(e); name != "" {
			keys = append(keys, name)
		}
	}
	return keys
}
