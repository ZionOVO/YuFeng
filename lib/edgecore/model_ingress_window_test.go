package edgecore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	modelsidev1 "yufeng/proto/gen/modelsidev1"
	unitv1 "yufeng/proto/gen/unitv1"
)

func TestModelIngressWindowKeepsNewestWhenItemLimitIsReached(t *testing.T) {
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(3, 1<<20, time.Minute))
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(2, 1<<20, time.Minute))
	for _, id := range []string{"request-1", "request-2", "request-3"} {
		if !queue.Offer(modelIngressTestItem(id, "profile-a", []byte(id))) {
			t.Fatalf("offer %s failed", id)
		}
	}
	batch, ok := queue.TakeBatch(context.Background(), 32, 4<<20, 0)
	if !ok {
		t.Fatal("latest items were not available")
	}
	if got := modelIngressBatchIDs(batch); len(got) != 2 || got[0] != "request-2" || got[1] != "request-3" {
		t.Fatalf("batch kept %v", got)
	}
	snapshot := queue.Snapshot()
	if snapshot.Drops.GetEvictedOldest() != 1 || snapshot.Dropped() != 1 {
		t.Fatalf("drop counters=%v", snapshot.Drops)
	}
	queue.CompleteBatch(batch, uint32(len(batch.Traffic)), false)
}

func TestModelIngressWindowChargesRetainedCapacityInsteadOfBodyLength(t *testing.T) {
	first := modelIngressTestItem("request-1", "profile-a", make([]byte, 1, 600<<10))
	second := modelIngressTestItem("request-2", "profile-b", make([]byte, 1, 600<<10))
	charge := modelIngressItemRetainedBytes(first)
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(4, charge*2-1, time.Minute))
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(4, charge*2-1, time.Minute))
	if !queue.Offer(first) || !queue.Offer(second) {
		t.Fatal("a newest item should replace the oldest item under the byte budget")
	}
	batch, ok := queue.TakeBatch(context.Background(), 4, 4<<20, 0)
	if !ok || len(batch.Traffic) != 1 || batch.Traffic[0].GetRequestId() != "request-2" {
		t.Fatalf("retained byte eviction batch=%v", modelIngressBatchIDs(batch))
	}
	if queue.Snapshot().Drops.GetEvictedOldest() != 1 {
		t.Fatalf("drops=%v", queue.Snapshot().Drops)
	}
	queue.CompleteBatch(batch, 1, false)
}

func TestModelIngressWindowExpiresQueuedItems(t *testing.T) {
	now := time.Unix(100, 0)
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(4, 1<<20, time.Second))
	queue.now = func() time.Time { return now }
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(4, 1<<20, time.Second))
	if !queue.Offer(modelIngressTestItem("expired", "profile-a", nil)) {
		t.Fatal("offer failed")
	}
	now = now.Add(2 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if batch, ok := queue.TakeBatch(ctx, 4, 4<<20, 0); ok || batch != nil {
		t.Fatal("expired item must not leave the window")
	}
	snapshot := queue.Snapshot()
	if snapshot.QueuedItems != 0 || snapshot.Drops.GetExpired() != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestModelIngressWindowSnapshotFinishesChunkedExpiryCleanup(t *testing.T) {
	now := time.Unix(100, 0)
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(128, 1<<20, time.Second))
	queue.now = func() time.Time { return now }
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(128, 1<<20, time.Second))
	for index := 0; index < 100; index++ {
		if !queue.Offer(modelIngressTestItem(fmt.Sprintf("expired-%03d", index), "profile-a", nil)) {
			t.Fatalf("offer %d failed", index)
		}
	}
	now = now.Add(2 * time.Second)
	snapshot := queue.Snapshot()
	if snapshot.QueuedItems != 0 || snapshot.Drops.GetExpired() != 100 {
		t.Fatalf("snapshot=%+v drops=%v", snapshot, snapshot.Drops)
	}
}

func TestModelIngressWindowDoesNotDisplaceInFlightTraffic(t *testing.T) {
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(1, 1<<20, time.Minute))
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(1, 1<<20, time.Minute))
	if !queue.Offer(modelIngressTestItem("in-flight", "profile-a", nil)) {
		t.Fatal("first offer failed")
	}
	batch, ok := queue.TakeBatch(context.Background(), 1, 4<<20, 0)
	if !ok {
		t.Fatal("first item was not leased")
	}
	if queue.Offer(modelIngressTestItem("new", "profile-a", nil)) {
		t.Fatal("new item cannot evict an in-flight item")
	}
	snapshot := queue.Snapshot()
	if snapshot.InFlightItems != 1 || snapshot.Drops.GetInFlightCapacity() != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	queue.CompleteBatch(batch, 1, false)
}

func TestModelIngressWindowShrinkConvergesWithoutBulkClear(t *testing.T) {
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(4, 1<<20, time.Minute))
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(3, 1<<20, time.Minute))
	for _, id := range []string{"request-1", "request-2", "request-3"} {
		if !queue.Offer(modelIngressTestItem(id, "profile-a", nil)) {
			t.Fatalf("offer %s failed", id)
		}
	}
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(1, 1<<20, time.Minute))
	if snapshot := queue.Snapshot(); snapshot.QueuedItems != 3 || snapshot.State != unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_CONVERGING {
		t.Fatalf("shrink must preserve existing entries while converging: %+v", snapshot)
	}
	if !queue.Offer(modelIngressTestItem("request-4", "profile-a", nil)) {
		t.Fatal("newest item should be admitted during convergence")
	}
	batch, ok := queue.TakeBatch(context.Background(), 4, 4<<20, 0)
	if !ok || len(batch.Traffic) != 1 || batch.Traffic[0].GetRequestId() != "request-4" {
		t.Fatalf("converged batch=%v", modelIngressBatchIDs(batch))
	}
	if snapshot := queue.Snapshot(); snapshot.Drops.GetEvictedOldest() != 3 {
		t.Fatalf("convergence drops=%v", snapshot.Drops)
	}
	queue.CompleteBatch(batch, 1, false)
}

func TestModelIngressWindowBoundsShrinkWorkOnTheRequestPath(t *testing.T) {
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(128, 1<<20, time.Minute))
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(128, 1<<20, time.Minute))
	for index := 0; index < 100; index++ {
		if !queue.Offer(modelIngressTestItem(fmt.Sprintf("request-%03d", index), "profile-a", nil)) {
			t.Fatalf("offer %d failed", index)
		}
	}
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(1, 1<<20, time.Minute))
	if !queue.Offer(modelIngressTestItem("newest", "profile-a", nil)) {
		t.Fatal("bounded convergence must keep the newest item")
	}
	snapshot := queue.Snapshot()
	if snapshot.Drops.GetEvictedOldest() != modelIngressRequestPathEvictionLimit || snapshot.QueuedItems != 101-modelIngressRequestPathEvictionLimit {
		t.Fatalf("snapshot=%+v drops=%v", snapshot, snapshot.Drops)
	}
	if snapshot.State != unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_CONVERGING {
		t.Fatalf("state=%v", snapshot.State)
	}
}

func TestModelIngressWindowDropsNewItemAfterAdmissionWorkBudget(t *testing.T) {
	large := modelIngressTestItem("large", "profile-a", make([]byte, 1<<20))
	windowBytes := modelIngressItemRetainedBytes(large)
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(128, windowBytes, time.Minute))
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(128, windowBytes, time.Minute))
	for index := 0; index < 64; index++ {
		if !queue.Offer(modelIngressTestItem(fmt.Sprintf("small-%02d", index), "profile-a", nil)) {
			t.Fatalf("small offer %d failed", index)
		}
	}
	if queue.Offer(large) {
		t.Fatal("large item must not make unbounded request-path eviction work")
	}
	snapshot := queue.Snapshot()
	if snapshot.Drops.GetEvictedOldest() != modelIngressRequestPathEvictionLimit || snapshot.Drops.GetAdmissionBudget() != 1 {
		t.Fatalf("drops=%v", snapshot.Drops)
	}
}

func TestModelIngressWindowReportsEveryLocalClamp(t *testing.T) {
	hard := modelIngressTestWindow(2, 1<<20, time.Second)
	queue := newModelIngressTestQueue(t, hard)
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(3, 2<<20, 2*time.Second))
	snapshot := queue.Snapshot()
	if snapshot.State != unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_DEGRADED {
		t.Fatalf("state=%v", snapshot.State)
	}
	if snapshot.Effective.GetMaxItems() != 2 || snapshot.Effective.GetMaxRetainedBytes() != 1<<20 || snapshot.Effective.GetMaxQueueAge().AsDuration() != time.Second {
		t.Fatalf("effective=%v", snapshot.Effective)
	}
	want := []unitv1.ModelIngressDegradationReason{
		unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_ITEMS,
		unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_RETAINED_BYTES,
		unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_QUEUE_AGE,
	}
	if len(snapshot.DegradationReasons) != len(want) {
		t.Fatalf("reasons=%v", snapshot.DegradationReasons)
	}
	for index := range want {
		if snapshot.DegradationReasons[index] != want[index] {
			t.Fatalf("reasons=%v", snapshot.DegradationReasons)
		}
	}
}

func TestModelIngressBatchStopsAtProfileAndBatchBounds(t *testing.T) {
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(8, 1<<20, time.Minute))
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(8, 1<<20, time.Minute))
	for _, item := range []*ModelIngressItem{
		modelIngressTestItem("a-1", "profile-a", []byte("1111")),
		modelIngressTestItem("a-2", "profile-a", []byte("2222")),
		modelIngressTestItem("b-1", "profile-b", []byte("3333")),
	} {
		if !queue.Offer(item) {
			t.Fatal("offer failed")
		}
	}
	batch, ok := queue.TakeBatch(context.Background(), 32, 4<<20, 0)
	if !ok || batch.ProfileDigest != "profile-a" || len(batch.Traffic) != 2 {
		t.Fatalf("first batch profile=%q traffic=%v", batch.ProfileDigest, modelIngressBatchIDs(batch))
	}
	queue.CompleteBatch(batch, 1, false)
	remaining, ok := queue.TakeBatch(context.Background(), 32, 4<<20, 0)
	if !ok || remaining.ProfileDigest != "profile-b" || len(remaining.Traffic) != 1 {
		t.Fatalf("remaining batch profile=%q traffic=%v", remaining.ProfileDigest, modelIngressBatchIDs(remaining))
	}
	snapshot := queue.Snapshot()
	if snapshot.Drops.GetModelsideRejected() != 1 || snapshot.Dropped() != 1 {
		t.Fatalf("completion drops=%v", snapshot.Drops)
	}
	queue.CompleteBatch(remaining, 1, false)
}

func TestModelIngressBatchDropsAnItemThatCannotFitAnyBatch(t *testing.T) {
	queue := newModelIngressTestQueue(t, modelIngressTestWindow(4, 1<<20, time.Minute))
	configureModelIngressTestQueue(t, queue, modelIngressTestWindow(4, 1<<20, time.Minute))
	if !queue.Offer(modelIngressTestItem("oversized", "profile-a", make([]byte, 2048))) ||
		!queue.Offer(modelIngressTestItem("fits", "profile-a", nil)) {
		t.Fatal("offer failed")
	}
	batch, ok := queue.TakeBatch(context.Background(), 4, 1024, 0)
	if !ok || len(batch.Traffic) != 1 || batch.Traffic[0].GetRequestId() != "fits" {
		t.Fatalf("batch=%v", modelIngressBatchIDs(batch))
	}
	if drops := queue.Snapshot().Drops; drops.GetItemTooLarge() != 1 {
		t.Fatalf("drops=%v", drops)
	}
	queue.CompleteBatch(batch, 1, false)
}

func newModelIngressTestQueue(t *testing.T, hard *artifactv1.ModelIngressWindow) *ModelIngressQueue {
	t.Helper()
	queue, err := NewModelIngressQueueWithHardLimit(hard)
	if err != nil {
		t.Fatal(err)
	}
	return queue
}

func configureModelIngressTestQueue(t *testing.T, queue *ModelIngressQueue, desired *artifactv1.ModelIngressWindow) {
	t.Helper()
	if err := queue.Configure(desired); err != nil {
		t.Fatal(err)
	}
}

func modelIngressTestWindow(items uint32, retainedBytes uint64, age time.Duration) *artifactv1.ModelIngressWindow {
	return &artifactv1.ModelIngressWindow{MaxItems: items, MaxRetainedBytes: retainedBytes, MaxQueueAge: durationpb.New(age)}
}

func modelIngressTestItem(id, profileDigest string, body []byte) *ModelIngressItem {
	return &ModelIngressItem{
		Profile: kernel.DefaultModelProfile(),
		Traffic: &modelsidev1.NormalizedTraffic{RequestId: id, ModelProfileDigest: profileDigest, Body: body},
	}
}

func modelIngressBatchIDs(batch *ModelIngressBatch) []string {
	if batch == nil {
		return nil
	}
	ids := make([]string, 0, len(batch.Traffic))
	for _, traffic := range batch.Traffic {
		ids = append(ids, traffic.GetRequestId())
	}
	return ids
}
