package kernel

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

func TestModelIngressWindowDefaultsMatchCapacityContract(t *testing.T) {
	desired := DefaultModelIngressWindow()
	if desired.GetMaxItems() != 4096 || desired.GetMaxRetainedBytes() != 128<<20 || desired.GetMaxQueueAge().AsDuration() != 2*time.Second {
		t.Fatalf("default model ingress window=%v", desired)
	}
	hard := DefaultModelIngressHardLimit()
	if hard.GetMaxItems() != 16384 || hard.GetMaxRetainedBytes() != 256<<20 || hard.GetMaxQueueAge().AsDuration() != 5*time.Minute {
		t.Fatalf("default model ingress hard limit=%v", hard)
	}
}

func TestNormalizeModelIngressWindowRejectsEveryInvalidBound(t *testing.T) {
	valid := DefaultModelIngressWindow()
	tests := []struct {
		name   string
		window *artifactv1.ModelIngressWindow
	}{
		{name: "missing"},
		{name: "zero items", window: &artifactv1.ModelIngressWindow{MaxRetainedBytes: valid.GetMaxRetainedBytes(), MaxQueueAge: valid.GetMaxQueueAge()}},
		{name: "too many items", window: &artifactv1.ModelIngressWindow{MaxItems: ModelIngressAbsoluteMaxItems + 1, MaxRetainedBytes: valid.GetMaxRetainedBytes(), MaxQueueAge: valid.GetMaxQueueAge()}},
		{name: "zero bytes", window: &artifactv1.ModelIngressWindow{MaxItems: valid.GetMaxItems(), MaxQueueAge: valid.GetMaxQueueAge()}},
		{name: "too few bytes", window: &artifactv1.ModelIngressWindow{MaxItems: valid.GetMaxItems(), MaxRetainedBytes: ModelIngressAbsoluteMinBytes - 1, MaxQueueAge: valid.GetMaxQueueAge()}},
		{name: "too many bytes", window: &artifactv1.ModelIngressWindow{MaxItems: valid.GetMaxItems(), MaxRetainedBytes: ModelIngressAbsoluteMaxBytes + 1, MaxQueueAge: valid.GetMaxQueueAge()}},
		{name: "age too short", window: &artifactv1.ModelIngressWindow{MaxItems: valid.GetMaxItems(), MaxRetainedBytes: valid.GetMaxRetainedBytes(), MaxQueueAge: durationpb.New(time.Millisecond)}},
		{name: "age too long", window: &artifactv1.ModelIngressWindow{MaxItems: valid.GetMaxItems(), MaxRetainedBytes: valid.GetMaxRetainedBytes(), MaxQueueAge: durationpb.New(ModelIngressAbsoluteMaxAge + time.Millisecond)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeModelIngressWindow(test.window); err == nil {
				t.Fatal("invalid model ingress window must fail")
			}
		})
	}
}

func TestModelIngressWindowOrDefaultDoesNotRetainCallerOwnedMessages(t *testing.T) {
	got, err := ModelIngressWindowOrDefault(nil)
	if err != nil {
		t.Fatal(err)
	}
	got.MaxItems = 1
	if DefaultModelIngressWindow().GetMaxItems() != 4096 {
		t.Fatal("default model ingress window must return a fresh value")
	}
}
