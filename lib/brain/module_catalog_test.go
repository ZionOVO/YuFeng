package brain

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/edgecore"
)

func TestCompiledDefenseModulesDeclareTrafficRequirements(t *testing.T) {
	registrations := compiledDefenseModules()
	if len(registrations) != 1 {
		t.Fatalf("compiled module count=%d want 1", len(registrations))
	}
	descriptor := registrations[0].descriptor()
	if descriptor.GetModuleId() != "traffic-interception" || descriptor.GetActive() {
		t.Fatalf("unexpected compiled descriptor: %+v", descriptor)
	}
	if !producerCapabilitiesCover(
		[]string{"traffic-review-candidate/v1", "traffic-window/v1", "unrelated/v1"},
		descriptor.GetRequiredProducerCapabilities(),
	) {
		t.Fatalf("requirements not satisfied: %v", descriptor.GetRequiredProducerCapabilities())
	}
}

func TestProducerCapabilitiesCoverRequiresEveryCapability(t *testing.T) {
	tests := []struct {
		name       string
		advertised []string
		required   []string
		want       bool
	}{
		{name: "all", advertised: []string{"a/v1", "b/v1"}, required: []string{"a/v1", "b/v1"}, want: true},
		{name: "missing", advertised: []string{"a/v1"}, required: []string{"a/v1", "b/v1"}},
		{name: "empty requirement", required: nil, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := producerCapabilitiesCover(test.advertised, test.required); got != test.want {
				t.Fatalf("producerCapabilitiesCover()=%v want %v", got, test.want)
			}
		})
	}
}

func TestModuleActivationReadsRegisteredEdgeCapabilities(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	suffix := newTestSuffix()
	required := "test-module-" + suffix + "/v1"
	server := NewModuleCatalogServer(st.Pool())
	active, err := server.edgeSupportsModule(ctx, []string{required})
	if err != nil || active {
		t.Fatalf("unadvertised module active=%v err=%v", active, err)
	}
	capabilities := edgecore.ProducerCapabilities()
	capabilities.ModuleCapabilities = append(capabilities.ModuleCapabilities, required)
	raw, err := protojson.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	unitID := "module-edge-" + suffix
	if _, err := st.Pool().Exec(ctx, `INSERT INTO units(unit_id, kind, token_hash, producer_capabilities, last_heartbeat_at)
		VALUES($1,'edge',$2,$3::jsonb,now())`, unitID, "hash-"+suffix, raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM units WHERE unit_id=$1`, unitID)
	})
	active, err = server.edgeSupportsModule(ctx, []string{required})
	if err != nil || !active {
		t.Fatalf("advertised module active=%v err=%v", active, err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE units SET last_heartbeat_at=now()-interval '3 minutes' WHERE unit_id=$1`, unitID); err != nil {
		t.Fatal(err)
	}
	active, err = server.edgeSupportsModule(ctx, []string{required})
	if err != nil || active {
		t.Fatalf("stale edge module active=%v err=%v", active, err)
	}
}
