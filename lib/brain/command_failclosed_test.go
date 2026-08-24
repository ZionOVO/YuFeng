package brain

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
	assetv1 "yufeng/proto/gen/assetv1"
	commandv1 "yufeng/proto/gen/commandv1"
	registryv1 "yufeng/proto/gen/registryv1"
)

func TestReportStepRequiresLeasedPhaseTransitions(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boot := "cmd-boot-" + newTestSuffix()
	reg := NewRegistryServer(st.Pool(), pub, boot)
	unit := "host-cmd-" + newTestSuffix()
	req := connect.NewRequest(&registryv1.RegisterRequest{
		UnitId: unit, Kind: registryv1.UnitKind_UNIT_KIND_HOST, Version: "t",
		ContractVersion: "v1", PubkeyHint: kernel.KeyID(pub),
		Asset: &assetv1.Asset{Id: unit, DisplayName: unit},
	})
	req.Header().Set("Authorization", "Bearer "+boot)
	sess, err := reg.Register(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	cmdID := "cmd-" + newTestSuffix()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO commands(command_id, unit_id, status, steps)
		VALUES($1,$2,'pending','[{"primitive":"sys.probe","args_json":"{}"}]')`, cmdID, unit); err != nil {
		t.Fatal(err)
	}
	svc := NewCommandServer(st.Pool())
	poll := connect.NewRequest(&commandv1.PollCommandsRequest{UnitId: unit})
	poll.Header().Set("Authorization", "Bearer "+sess.Msg.Token)
	leased, err := svc.PollCommands(ctx, poll)
	if err != nil || len(leased.Msg.GetCommands()) != 1 {
		t.Fatalf("lease command: response=%v err=%v", leased, err)
	}
	command := leased.Msg.GetCommands()[0]
	ok := connect.NewRequest(&commandv1.ReportStepRequest{
		CommandId: cmdID, UnitId: unit, LeaseId: command.GetLeaseId(), LeaseEpoch: command.GetLeaseEpoch(),
		Receipts: []*commandv1.StepReceipt{{StepIndex: 0, Phase: commandv1.StepPhase_STEP_PHASE_SUCCEEDED}},
	})
	ok.Header().Set("Authorization", "Bearer "+sess.Msg.Token)
	if _, err := svc.ReportStep(ctx, ok); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("SUCCEEDED want failed_precondition got %v", err)
	}
	for _, phase := range []commandv1.StepPhase{
		commandv1.StepPhase_STEP_PHASE_INTENT_RECORDED,
		commandv1.StepPhase_STEP_PHASE_EFFECT_STARTED,
		commandv1.StepPhase_STEP_PHASE_FAILED,
	} {
		report := connect.NewRequest(&commandv1.ReportStepRequest{
			CommandId: cmdID, UnitId: unit, LeaseId: command.GetLeaseId(), LeaseEpoch: command.GetLeaseEpoch(),
			Receipts: []*commandv1.StepReceipt{{StepIndex: 0, Phase: phase, Error: "controlled failure"}},
		})
		report.Header().Set("Authorization", "Bearer "+sess.Msg.Token)
		if _, err := svc.ReportStep(ctx, report); err != nil {
			t.Fatalf("phase %s must persist: %v", phase, err)
		}
	}
	var status string
	if err := st.Pool().QueryRow(ctx, `SELECT status FROM commands WHERE command_id=$1`, cmdID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("command status=%s want failed", status)
	}
}
