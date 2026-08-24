package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/edgeclient"
	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
	telemetryv1 "yufeng/proto/gen/telemetryv1"
)

// 发布缓存的落盘/装载回环：装载走 ApplySnapshot，验签必须通过，
// 装载后的发布集与写入时一致。
func TestSessionPersistRoundTrip(t *testing.T) {
	path := t.TempDir() + "/sess.json"
	in := &edgeclient.Session{UnitID: "u", AssetID: "a", Token: "tok", Refresh: "ref", HeartbeatInterval: 30 * time.Second}
	if err := saveSession(path, in); err != nil {
		t.Fatal(err)
	}
	got := loadSession(path)
	if got == nil || got.UnitID != "u" || got.Refresh != "ref" || got.Token != "tok" {
		t.Fatalf("session %+v", got)
	}
}

func TestOfflineStartRequiresCache(t *testing.T) {
	if err := decideOfflineStart(false, false, errOffline); err == nil {
		t.Fatal("no verified caches and brain down must fail")
	}
	if err := decideOfflineStart(true, false, errOffline); err == nil {
		t.Fatal("generation without listen plan must not bind offline")
	}
	if err := decideOfflineStart(true, true, errOffline); err != nil {
		t.Fatal("verified generation and listen plan must serve offline")
	}
	if err := decideOfflineStart(false, false, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUploadEventBatchesHonorsProtocolLimit(t *testing.T) {
	var batchSizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var body telemetryv1.UploadEventsRequest
		if err := proto.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		batchSizes = append(batchSizes, len(body.Events))
		response, err := proto.Marshal(&telemetryv1.UploadEventsResponse{Accepted: int32(len(body.Events))})
		if err != nil {
			t.Errorf("marshal response: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/proto")
		if _, err := w.Write(response); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()
	client := edgeclient.New(server.URL, server.Client())
	session := &edgeclient.Session{UnitID: "unit", Token: "token"}
	spool, err := edgeclient.NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	events := make([]*eventv1.Event, 0, kernel.UploadBatchMax*2+1)
	for i := 0; i < kernel.UploadBatchMax*2+1; i++ {
		event := &eventv1.Event{Id: fmt.Sprintf("event-%03d", i)}
		events = append(events, event)
		if err := spool.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	files, err := spool.Files()
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	if err := uploadEventBatches(context.Background(), client, session, spool, files[0], events); err != nil {
		t.Fatal(err)
	}
	want := []int{kernel.UploadBatchMax, kernel.UploadBatchMax, 1}
	if fmt.Sprint(batchSizes) != fmt.Sprint(want) {
		t.Fatalf("batch sizes=%v want=%v", batchSizes, want)
	}
	if files, err := spool.Files(); err != nil || len(files) != 0 {
		t.Fatalf("accepted spool files=%v err=%v", files, err)
	}
}

var errOffline = errString("brain down")

type errString string

func (e errString) Error() string { return string(e) }

func TestGenerationCacheRoundTripKeepsCurrentAndPrevious(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := edgecore.MarshalRules([]edgecore.Rule{{ID: "sql-union", Pattern: `(?i)union\s+select`}})
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: edgecore.RulePayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(), CreatedBy: "test",
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	previous := signedCacheGeneration(t, priv, "gen-1", "", 1, a)
	current := signedCacheGeneration(t, priv, "gen-2", "gen-1", 2, a)
	cachePath := t.TempDir() + "/generation-cache.json"
	if err := saveGenerationCache(cachePath, previous, current); err != nil {
		t.Fatal(err)
	}

	set := edgecore.NewReleaseSet()
	if !loadGenerationCache(cachePath, set, pub) {
		t.Fatal("expected verified cache")
	}
	if set.CurrentGenerationSeq() != 2 {
		t.Fatalf("generation=%d", set.CurrentGenerationSeq())
	}
	counters := set.Counters()
	if len(counters) != 1 || counters[0].ReleaseID != "rel-1" {
		t.Fatalf("缓存装载结果异常: %+v", counters)
	}
}

// 缓存装载必须验签：被篡改的缓存不能进入发布集。
func TestGenerationCacheFallsBackWhenCurrentIsTampered(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := edgecore.MarshalRules([]edgecore.Rule{{ID: "r", Pattern: `x`}})
	if err != nil {
		t.Fatal(err)
	}
	a := &artifactv1.Artifact{
		Kind: artifactv1.Kind_KIND_RULE, Payload: payload, PayloadSchema: edgecore.RulePayloadSchema,
		Ttl: durationpb.New(time.Hour), CreatedAt: timestamppb.Now(), CreatedBy: "test",
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		t.Fatal(err)
	}
	previous := signedCacheGeneration(t, priv, "gen-1", "", 1, a)
	current := signedCacheGeneration(t, priv, "gen-2", "gen-1", 2, a)
	current.Members[0].Artifact.Payload = []byte("tampered")
	cachePath := t.TempDir() + "/generation-cache.json"
	if err := saveGenerationCache(cachePath, previous, current); err != nil {
		t.Fatal(err)
	}

	set := edgecore.NewReleaseSet()
	if !loadGenerationCache(cachePath, set, pub) {
		t.Fatal("previous generation should remain usable")
	}
	if got := set.CurrentGenerationSeq(); got != 1 {
		t.Fatalf("current generation=%d, want fallback 1", got)
	}
}

func signedCacheGeneration(t *testing.T, priv ed25519.PrivateKey, id, parent string, seq int64, a *artifactv1.Artifact) *artifactv1.AssetGeneration {
	t.Helper()
	gen := &artifactv1.AssetGeneration{
		GenerationId: id, AssetId: "asset-1", GenerationSeq: seq, ParentGenerationId: parent,
		MinEdgeVersion: kernel.MinimumEdgeVersion, NotBefore: timestamppb.Now(),
		Members: []*artifactv1.ReleaseItem{{ReleaseId: "rel-1", Artifact: proto.Clone(a).(*artifactv1.Artifact), Mode: commonv1.ReleaseMode_RELEASE_MODE_ENFORCE}},
	}
	if err := kernel.SignGeneration(gen, priv); err != nil {
		t.Fatal(err)
	}
	return gen
}
