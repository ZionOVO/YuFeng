//go:build linux && amd64

package runtime

func linuxAuditArchitecture() (uint32, bool) {
	return 0xc000003e, true
}
