package store

import (
	"strings"
	"testing"
)

func TestSyntheticInferenceMigrationDeletesOnlyKnownFixedScorerRows(t *testing.T) {
	sql := readMigration(t, "00038_remove_synthetic_model_inferences.sql")
	for _, required := range []string{
		"DELETE FROM model_inferences",
		"model_group = 'fake'",
		"model_type = 'http'",
		"model_version = 'v1'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("synthetic inference cleanup must contain %q", required)
		}
	}
}
