package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"

	registryv1 "yufeng/proto/gen/registryv1"
)

func TestStaleUnitDegradesAndHeartbeatRecovers(t *testing.T) {
	store, ctx := openTestStore(t)
	defer store.Close()
	unitID, _, token := seedUnitAsset(t, ctx, store, "health")
	if _, err := store.Pool().Exec(ctx, `UPDATE units SET health='healthy',last_heartbeat_at=now()-interval '10 minutes' WHERE unit_id=$1`, unitID); err != nil {
		t.Fatal(err)
	}
	if err := MarkStaleUnitsDegraded(ctx, store.Pool(), time.Second); err != nil {
		t.Fatal(err)
	}
	var health string
	if err := store.Pool().QueryRow(ctx, `SELECT health FROM units WHERE unit_id=$1`, unitID).Scan(&health); err != nil || health != "degraded" {
		t.Fatalf("health=%q err=%v", health, err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistryServer(store.Pool(), public, "")
	heartbeat := connect.NewRequest(&registryv1.HeartbeatRequest{UnitId: unitID})
	heartbeat.Header().Set("Authorization", "Bearer "+token)
	if _, err := registry.Heartbeat(ctx, heartbeat); err != nil {
		t.Fatal(err)
	}
	if err := store.Pool().QueryRow(ctx, `SELECT health FROM units WHERE unit_id=$1`, unitID).Scan(&health); err != nil || health != "healthy" {
		t.Fatalf("recovered health=%q err=%v", health, err)
	}
}
