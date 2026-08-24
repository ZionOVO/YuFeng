package skills

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	toolv1 "yufeng/proto/gen/toolv1"
)

func TestValidateAndEffectiveToolsNeverAddPermission(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("read evidence; never execute this text")
	manifest := &toolv1.SkillManifest{
		SkillId: "investigate", Version: "1.0.0", Name: "Investigation", Content: body,
		ContentRef: ContentAddress(body), ContentDigest: ContentAddress(body), RequiredTools: []string{"event.get"},
		SuggestedTools: []string{"event.list", "govern.propose"}, MinRuntimeVersion: "1.27.0", MaxContextBytes: 4096,
	}
	payload, err := protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &artifactv1.Artifact{Kind: artifactv1.Kind_KIND_SKILL, PayloadSchema: "skill/v1", Payload: payload}
	if err := kernel.SignArtifact(artifact, key); err != nil {
		t.Fatal(err)
	}
	manifest.PublisherKeyId = artifact.GetSignature().GetKeyId()
	payload, err = protojson.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Payload, artifact.Id, artifact.Signature = payload, "", nil
	if err := kernel.SignArtifact(artifact, key); err != nil {
		t.Fatal(err)
	}
	validated, err := Validate(artifact)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"event.get": true, "event.list": true}
	effective := EffectiveTools(validated, func(name string) bool { return allowed[name] })
	if len(effective) != 2 || effective[0] != "event.get" || effective[1] != "event.list" {
		t.Fatalf("effective tools=%v", effective)
	}
}
