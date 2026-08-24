package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
	assetv1 "yufeng/proto/gen/assetv1"
	registryv1 "yufeng/proto/gen/registryv1"
)

func TestRegisterCannotHijackExistingUnit(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "unit-boot-" + newTestSuffix()
	reg := NewRegistryServer(st.Pool(), pub, boot)
	unit := "unit-hijack-" + newTestSuffix()
	first := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: unit, Kind: registryv1.UnitKind_UNIT_KIND_EDGE, Version: "t",
		ContractVersion: "v1", PubkeyHint: kernel.KeyID(pub),
		Asset: &assetv1.Asset{Id: unit, DisplayName: unit}, Capabilities: testEdgeCapabilities(),
	})
	first.Header().Set("Authorization", "Bearer "+boot)
	ok, err := reg.Register(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	var accessHash, refreshHash string
	if err := st.Pool().QueryRow(ctx, `SELECT token_hash, refresh_token_hash FROM units WHERE unit_id=$1`, unit).Scan(&accessHash, &refreshHash); err != nil {
		t.Fatal(err)
	}

	naked := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: unit, Kind: registryv1.UnitKind_UNIT_KIND_EDGE, Version: "evil",
		ContractVersion: "v1", PubkeyHint: kernel.KeyID(pub),
	})
	if _, err := reg.Register(ctx, naked); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous want unauthenticated got %v", err)
	}
	bootAgain := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: unit, Kind: registryv1.UnitKind_UNIT_KIND_EDGE, Version: "evil",
		ContractVersion: "v1", PubkeyHint: kernel.KeyID(pub),
	})
	bootAgain.Header().Set("Authorization", "Bearer "+boot)
	if _, err := reg.Register(ctx, bootAgain); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("bootstrap overwrite want permission_denied got %v", err)
	}
	var access2, refresh2 string
	if err := st.Pool().QueryRow(ctx, `SELECT token_hash, refresh_token_hash FROM units WHERE unit_id=$1`, unit).Scan(&access2, &refresh2); err != nil {
		t.Fatal(err)
	}
	if access2 != accessHash || refresh2 != refreshHash {
		t.Fatal("hijack must not rotate access or refresh hashes")
	}
	_ = ok
}
