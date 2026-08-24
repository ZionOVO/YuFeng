//go:build linux && arm64

package runtime

func linuxAuditArchitecture() (uint32, bool) {
	return 0xc00000b7, true
}
