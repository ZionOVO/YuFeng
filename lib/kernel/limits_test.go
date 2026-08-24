package kernel

import (
	"testing"
	"time"
)

func TestResourceLimitsMatchFrozenContract(t *testing.T) {
	// 这些断言把 architecture.md §13 的数字钉在同一标识符上。
	if ShadowMinDuration.Seconds() != 300 {
		t.Fatalf("ShadowMinDuration=%s", ShadowMinDuration)
	}
	if ShadowMinRequests != 100 || CanaryMinRequests != 100 {
		t.Fatalf("min requests shadow=%d canary=%d", ShadowMinRequests, CanaryMinRequests)
	}
	if CanaryPercentDefault != 5 {
		t.Fatalf("CanaryPercentDefault=%d", CanaryPercentDefault)
	}
	if ExtAuthzTimeout.Milliseconds() != 50 {
		t.Fatalf("ExtAuthzTimeout=%s", ExtAuthzTimeout)
	}
	if EngineBodyLimitBytes != 64*1024 {
		t.Fatalf("EngineBodyLimitBytes=%d", EngineBodyLimitBytes)
	}
	if ClockSkew.Seconds() != 60 {
		t.Fatalf("ClockSkew=%s", ClockSkew)
	}
	if NoDetectionSampleRate != 0.01 {
		t.Fatalf("NoDetectionSampleRate=%v", NoDetectionSampleRate)
	}
	if EvidenceRingTTL.Minutes() != 15 {
		t.Fatalf("EvidenceRingTTL=%s", EvidenceRingTTL)
	}
	if P99ExtraLatency.Milliseconds() != 5 {
		t.Fatalf("P99ExtraLatency=%s", P99ExtraLatency)
	}
	if EdgeThroughputRPS != 2000 {
		t.Fatalf("EdgeThroughputRPS=%d", EdgeThroughputRPS)
	}
	if ExtAuthzTimeoutRateTrip != 0.05 {
		t.Fatalf("ExtAuthzTimeoutRateTrip=%v", ExtAuthzTimeoutRateTrip)
	}
	if CRSVersion != "4.25.0" || len(CRSTarballSHA256) != 64 {
		t.Fatalf("crs pin version=%s sha=%s", CRSVersion, CRSTarballSHA256)
	}
	if CRSParanoia != 1 {
		t.Fatalf("CRSParanoia=%d", CRSParanoia)
	}
	if BackupRestoreDeadline != time.Hour {
		t.Fatalf("BackupRestoreDeadline=%s", BackupRestoreDeadline)
	}
	if BackupCommittedRPO != 0 {
		t.Fatalf("BackupCommittedRPO=%d", BackupCommittedRPO)
	}
	if JarvisOnlineWindow.Seconds() != 60 {
		t.Fatalf("JarvisOnlineWindow=%s", JarvisOnlineWindow)
	}
	if EdgeOnlineWindow.Seconds() != 90 {
		t.Fatalf("EdgeOnlineWindow=%s", EdgeOnlineWindow)
	}
	if ModelBypassP99Budget != time.Millisecond {
		t.Fatalf("ModelBypassP99Budget=%s", ModelBypassP99Budget)
	}
	if ModelBypassCPUPercentBudget != 5 {
		t.Fatalf("ModelBypassCPUPercentBudget=%v", ModelBypassCPUPercentBudget)
	}
	if ModelIngressDefaultItems != 4096 || ModelIngressDefaultBytes != 128<<20 || ModelIngressDefaultAge != 2*time.Second {
		t.Fatalf("model ingress default items=%d bytes=%d age=%s", ModelIngressDefaultItems, ModelIngressDefaultBytes, ModelIngressDefaultAge)
	}
	if ModelIngressLocalMaxItems != 16384 || ModelIngressLocalMaxBytes != 256<<20 || ModelIngressLocalMaxAge != 5*time.Minute {
		t.Fatalf("model ingress local max items=%d bytes=%d age=%s", ModelIngressLocalMaxItems, ModelIngressLocalMaxBytes, ModelIngressLocalMaxAge)
	}
	if ModelIngressAbsoluteMaxItems != 65536 || ModelIngressAbsoluteMinBytes != 1<<20 || ModelIngressAbsoluteMaxBytes != 256<<20 || ModelIngressAbsoluteMinAge != 10*time.Millisecond || ModelIngressAbsoluteMaxAge != 5*time.Minute {
		t.Fatalf("model ingress absolute items=%d bytes=%d..%d age=%s..%s", ModelIngressAbsoluteMaxItems, ModelIngressAbsoluteMinBytes, ModelIngressAbsoluteMaxBytes, ModelIngressAbsoluteMinAge, ModelIngressAbsoluteMaxAge)
	}
	if ModelIngressBatchMaxItems != 32 || ModelIngressBatchMaxBytes != 4<<20 || ModelIngressBatchWait != 10*time.Millisecond || ModelSideIngressWorkers != 2 {
		t.Fatalf("model ingress batch items=%d bytes=%d wait=%s workers=%d", ModelIngressBatchMaxItems, ModelIngressBatchMaxBytes, ModelIngressBatchWait, ModelSideIngressWorkers)
	}
	if ModelSideIngressReceiveMaxBytes != 10<<20 || ModelSideResultQueueMax != 1024 || ModelSideUploadBatchMax != 100 {
		t.Fatalf("modelside receive=%d results=%d upload batch=%d", ModelSideIngressReceiveMaxBytes, ModelSideResultQueueMax, ModelSideUploadBatchMax)
	}
	if ModelReviewWindow != 5*time.Minute || ModelReviewPerUnit != 4 || ModelReviewPerRoute != 1 {
		t.Fatalf("model review window=%s unit=%d route=%d", ModelReviewWindow, ModelReviewPerUnit, ModelReviewPerRoute)
	}
	if DefaultChatModel != "grok-4-1-fast-non-reasoning" {
		t.Fatalf("DefaultChatModel=%s", DefaultChatModel)
	}
	if ChatProbeMaxTokens != 32 {
		t.Fatalf("ChatProbeMaxTokens=%d", ChatProbeMaxTokens)
	}
	if ChatCompleteMaxTokens != 1024 {
		t.Fatalf("ChatCompleteMaxTokens=%d", ChatCompleteMaxTokens)
	}
	if TrafficReviewModelInputTokens != 8192 || TrafficReviewModelInputBytes != 32<<10 || TrafficReviewModelEvidenceBytes != 30<<10 {
		t.Fatalf("traffic review model input tokens=%d bytes=%d evidence=%d", TrafficReviewModelInputTokens, TrafficReviewModelInputBytes, TrafficReviewModelEvidenceBytes)
	}
	if ChatCompleteTimeout != 60*time.Second {
		t.Fatalf("ChatCompleteTimeout=%s", ChatCompleteTimeout)
	}
	if ArtifactPageMaxBytes != 4<<20 || ArtifactPageHardMaxBytes != 16<<20 {
		t.Fatalf("artifact page bytes default=%d hard=%d", ArtifactPageMaxBytes, ArtifactPageHardMaxBytes)
	}
	if IdempotencyPendingTTL != 120*time.Second {
		t.Fatalf("IdempotencyPendingTTL=%s", IdempotencyPendingTTL)
	}
	if IdempotencyPendingTTL <= ChatCompleteTimeout {
		t.Fatal("IdempotencyPendingTTL must exceed longest pending write")
	}
	if ModelGatewayStatsWindow != 24*time.Hour {
		t.Fatalf("ModelGatewayStatsWindow=%s", ModelGatewayStatsWindow)
	}
	if ModelGatewayCallRetain != 7*24*time.Hour {
		t.Fatalf("ModelGatewayCallRetain=%s", ModelGatewayCallRetain)
	}
	if SessionLongPollDefault.Seconds() != 30 {
		t.Fatalf("SessionLongPollDefault=%s", SessionLongPollDefault)
	}
	if SessionLongPollMax.Seconds() != 60 {
		t.Fatalf("SessionLongPollMax=%s", SessionLongPollMax)
	}
	if AgentLongPollDefault.Seconds() != 30 {
		t.Fatalf("AgentLongPollDefault=%s", AgentLongPollDefault)
	}
	if AgentLongPollMax.Seconds() != 60 {
		t.Fatalf("AgentLongPollMax=%s", AgentLongPollMax)
	}
	if DataplaneControlPort != 19091 {
		t.Fatalf("DataplaneControlPort=%d", DataplaneControlPort)
	}
	if DefaultModelSideSocket != "/run/yufeng/modelside.sock" {
		t.Fatalf("DefaultModelSideSocket=%s", DefaultModelSideSocket)
	}
}

func TestResolveLongPollUsesPairedMaxNotLegacy(t *testing.T) {
	cases := []struct {
		name    string
		seconds int32
		def     time.Duration
		max     time.Duration
		want    time.Duration
		fail    bool
	}{
		{name: "omit uses default", seconds: 0, def: SessionLongPollDefault, max: SessionLongPollMax, want: SessionLongPollDefault},
		{name: "session 45 under 60", seconds: 45, def: SessionLongPollDefault, max: SessionLongPollMax, want: 45 * time.Second},
		{name: "agent 45 under 60", seconds: 45, def: AgentLongPollDefault, max: AgentLongPollMax, want: 45 * time.Second},
		{name: "session over max", seconds: 61, def: SessionLongPollDefault, max: SessionLongPollMax, fail: true},
		{name: "agent over max", seconds: 61, def: AgentLongPollDefault, max: AgentLongPollMax, fail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveLongPoll(tc.seconds, tc.def, tc.max)
			if tc.fail {
				if err == nil {
					t.Fatal("over max must fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	if _, err := ResolveLongPoll(45, AgentLongPollDefault, LongPollMax); err == nil {
		t.Fatal("legacy 30 second maximum must reject a 45 second poll")
	}
	wait, err := ResolveLongPoll(45, AgentLongPollDefault, AgentLongPollMax)
	if err != nil || wait != 45*time.Second {
		t.Fatalf("AgentLongPollMax must accept 45s: wait=%s err=%v", wait, err)
	}
	if LongPollMax != 30*time.Second {
		t.Fatalf("LongPollMax still 30s historical, got %s", LongPollMax)
	}
	if 45*time.Second <= LongPollMax {
		t.Fatal("test setup: 45s must exceed LongPollMax so we prove it is not the gate")
	}
}
