//go:build windows

package runtime

import (
	"context"
	"errors"
	"os/exec"

	"golang.org/x/sys/windows"
)

func verifiedSandboxCapabilities() []string {
	if err := createRestrictedToken.Find(); err != nil {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil
	}
	_ = windows.CloseHandle(job)
	return []string{"restricted_token", "job_object"}
}

func applyInvestigationSandbox() error {
	return errors.New("verified restricted token, appcontainer and job object are unavailable")
}

func platformSandboxCommand(ctx context.Context, bin string, _ []string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}
