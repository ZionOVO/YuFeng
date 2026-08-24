//go:build linux && !amd64 && !arm64

package runtime

func linuxAuditArchitecture() (uint32, bool) {
	return 0, false
}
