package brain

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	agentv1 "yufeng/proto/gen/agentv1"
	eventv1 "yufeng/proto/gen/eventv1"
	governv1 "yufeng/proto/gen/governv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
)

func TestValidateIncomingCanaryAndTTL(t *testing.T) {
	if err := validateIncoming(&governv1.PromoteCanaryRequest{CanaryPercent: 99}); err == nil {
		t.Fatal("canary 99 must be invalid_argument before business")
	}
	if err := validateIncoming(&governv1.PromoteCanaryRequest{CanaryPercent: 5}); err != nil {
		t.Fatal(err)
	}
	if err := validateIncoming(&governv1.ProposeArtifactRequest{Ttl: durationpb.New(time.Second)}); err == nil {
		t.Fatal("ttl 1s must fail")
	}
	if err := validateIncoming(&governv1.ProposeArtifactRequest{Ttl: durationpb.New(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateIncomingIllegalEventRegisterTool(t *testing.T) {
	cases := []struct {
		name string
		msg  proto.Message
	}{
		{name: "event empty id", msg: &eventv1.Event{AssetId: "a", OccurredAt: timestamppb.Now()}},
		{name: "event missing occurred_at", msg: &eventv1.Event{Id: "e1", AssetId: "a"}},
		{name: "event empty asset", msg: &eventv1.Event{Id: "e1", OccurredAt: timestamppb.Now()}},
		{name: "register empty agent", msg: &agentv1.RegisterAgentRequest{BootstrapToken: "t"}},
		{name: "register empty token", msg: &agentv1.RegisterAgentRequest{AgentId: "a1"}},
		{name: "register empty public key", msg: &agentv1.RegisterAgentRequest{AgentId: "a1", BootstrapToken: "t"}},
		{name: "tool empty name", msg: &toolgatewayv1.InvokeToolRequest{ArgsJson: `{}`}},
		{name: "tool bad json", msg: &toolgatewayv1.InvokeToolRequest{ToolName: "event.get", ArgsJson: `{`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateIncoming(tc.msg); err == nil {
				t.Fatal("illegal request must fail before business")
			}
		})
	}
	if err := validateIncoming(&eventv1.Event{Id: "e1", AssetId: "a", OccurredAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := validateIncoming(&agentv1.RegisterAgentRequest{AgentId: "a1", BootstrapToken: "t", AgentPublicKey: "pub"}); err != nil {
		t.Fatal(err)
	}
	if err := validateIncoming(&toolgatewayv1.InvokeToolRequest{ToolName: "event.get", ArgsJson: `{"event_id":"e"}`}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateInterceptorRejectsBeforeNext(t *testing.T) {
	called := false
	next := func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	}
	fn := ValidateInterceptor()(next)
	req := connect.NewRequest(&agentv1.RegisterAgentRequest{})
	_, err := fn(context.Background(), req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("want invalid_argument got %v", err)
	}
	if called {
		t.Fatal("interceptor must not reach business")
	}
}

func TestValidateSchedulerConfigProduction(t *testing.T) {
	if err := ValidateSchedulerConfig(SchedulerConfig{}, false); err == nil {
		t.Fatal("missing production gates must refuse start")
	}
	if err := ValidateSchedulerConfig(SchedulerConfig{}, true); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchedulerConfig(ProductionScheduler(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	bad := ProductionScheduler(time.Minute)
	bad.CanaryPercent = kernel.CanaryPercentMax + 1
	if err := ValidateSchedulerConfig(bad, false); err == nil {
		t.Fatal("illegal percent must refuse")
	}
}
