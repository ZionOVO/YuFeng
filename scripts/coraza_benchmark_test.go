package scripts

import (
	"strings"
	"testing"
)

func TestCorazaBenchmarkUsesIsolatedOfficialAndMaintainedSources(t *testing.T) {
	body := readScript(t, "coraza-benchmark.ps1")
	for _, want := range []string{
		"[string]$Benchtime = '3s'",
		"[int]$Count = 5",
		"[int[]]$ProcessorCounts = @(1, 32)",
		"[string]$ResumeDirectory = ''",
		"git archive",
		".tmp",
		"github.com/corazawaf/coraza/v3",
		"github.com/ZionOVO/coraza/v3@v3.7.0-zion.1",
		"SecRxPreFilter On",
		"official source copy did not contain SecRxPreFilter On",
		"go mod edit -dropreplace",
		"go mod verify",
		"BenchmarkCoraza(DetectorSerial|DetectorParallel|ReleaseProxyCapacityParallel)",
		"median_ns_per_operation",
		"requests_per_second",
		"requests_per_second_change_percent",
		"elseif ($ConfiguredProcessors -contains 1)",
		"Reusing completed capacity benchmark",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Coraza benchmark script missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`C:\Workspace\Coraza`,
		"YUFENG_UPDATE_CORAZA_GOLDEN",
		"git checkout",
		"git reset",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Coraza benchmark script must not mutate sources through %q", forbidden)
		}
	}
}
