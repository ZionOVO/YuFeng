package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"yufeng/lib/kernel"
	eventv1 "yufeng/proto/gen/eventv1"
	workerv1 "yufeng/proto/gen/workerv1"
)

func investigationInputForWork(ctx context.Context, db dbTX, workID string) (string, *workerv1.InvestigationInput, error) {
	var runID, eventID, ticketDigest, clusterID, caseID, candidateID, sensitiveRef, storedDigest, status string
	var raw []byte
	err := db.QueryRow(ctx, `SELECT w.run_id, w.investigation_event_id, w.investigation_ticket_digest,
		w.investigation_cluster_id, w.investigation_case_id, w.review_candidate_id, w.sensitive_content_ref,
		COALESCE(t.ticket,'{}'::jsonb), COALESCE(t.ticket_digest,''), COALESCE(t.status,'')
		FROM work_items w LEFT JOIN check_tickets t ON t.event_id=w.investigation_event_id WHERE w.work_id=$1`, workID).
		Scan(&runID, &eventID, &ticketDigest, &clusterID, &caseID, &candidateID, &sensitiveRef, &raw, &storedDigest, &status)
	if err != nil {
		return "", nil, err
	}
	if eventID == "" {
		if caseID != "" {
			return runID, &workerv1.InvestigationInput{CaseId: caseID, ReviewCandidateId: candidateID, SensitiveContentRef: sensitiveRef}, nil
		}
		return runID, nil, nil
	}
	if status != checkTicketReady || ticketDigest == "" || ticketDigest != storedDigest {
		return "", nil, errors.New("investigation work ticket is not ready")
	}
	var ticket eventv1.CheckTicket
	if err := protojson.Unmarshal(raw, &ticket); err != nil {
		return "", nil, err
	}
	digest, err := kernel.CheckTicketDigest(&ticket)
	if err != nil || digest != ticketDigest {
		return "", nil, errors.New("investigation work ticket digest mismatch")
	}
	return runID, &workerv1.InvestigationInput{Ticket: &ticket, TicketDigest: ticketDigest, ClusterId: clusterID}, nil
}

func validateInvestigationCompletion(ctx context.Context, tx pgx.Tx, workID, resultRef, receiptJSON string) (bool, error) {
	runID, input, err := investigationInputForWork(ctx, tx, workID)
	if err != nil {
		return false, err
	}
	if input == nil {
		return false, nil
	}
	if input.GetCaseId() != "" {
		var receipt struct {
			Status               string `json:"status"`
			TrafficFindingDigest string `json:"traffic_finding_digest"`
		}
		if err := json.Unmarshal([]byte(receiptJSON), &receipt); err != nil || receipt.Status != "ok" || receipt.TrafficFindingDigest == "" || receipt.TrafficFindingDigest != resultRef {
			return true, connect.NewError(connect.CodeFailedPrecondition, errors.New("traffic investigation completion receipt is invalid"))
		}
		var ready int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM investigation_cases c
			JOIN work_items w ON w.investigation_case_id=c.case_id
			JOIN model_generations g ON g.turn_id=w.turn_id
			WHERE w.work_id=$1 AND c.state IN ('finding_ready','shadow_observing') AND g.state='completed' AND g.sensitive AND g.case_id=c.case_id`, workID).Scan(&ready); err != nil {
			return true, err
		}
		if ready != 1 {
			return true, connect.NewError(connect.CodeFailedPrecondition, errors.New("traffic finding is not accepted for case"))
		}
		return true, nil
	}
	var receipt workerv1.InvestigationReceipt
	if err := protojson.Unmarshal([]byte(receiptJSON), &receipt); err != nil {
		return true, connect.NewError(connect.CodeInvalidArgument, errors.New("investigation completion receipt is invalid"))
	}
	if err := kernel.ValidateInvestigationReceipt(input, &receipt); err != nil {
		return true, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if err := validateInvestigationReads(ctx, tx, runID, receipt.GetReads()); err != nil {
		return true, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if resultRef != receipt.GetOutputDigest() {
		return true, connect.NewError(connect.CodeFailedPrecondition, errors.New("investigation result reference does not match receipt"))
	}
	return true, insertInvestigationReceipt(ctx, tx, runID, workID, input, &receipt)
}

func validateInvestigationReads(ctx context.Context, db dbTX, runID string, reads []*workerv1.InvestigationToolRead) error {
	rows, err := db.Query(ctx, `SELECT tool_name, result_digest FROM tool_invocations
		WHERE run_id=$1 AND state='settled' AND outcome='succeeded'`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	actual := make(map[string]string, len(reads))
	count := 0
	for rows.Next() {
		var name, digest string
		if err := rows.Scan(&name, &digest); err != nil {
			return err
		}
		if _, duplicated := actual[name]; duplicated {
			return errors.New("investigation tool read is duplicated in audit ledger")
		}
		actual[name] = digest
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(reads) {
		return errors.New("investigation receipt does not match audit ledger")
	}
	for _, read := range reads {
		if read == nil || actual[read.GetToolName()] != read.GetResultDigest() {
			return errors.New("investigation receipt does not match audit ledger")
		}
	}
	return nil
}

func recordInvestigationTerminal(ctx context.Context, tx pgx.Tx, workID, status, errorCode, message string) error {
	runID, input, err := investigationInputForWork(ctx, tx, workID)
	if err != nil || input == nil {
		return err
	}
	if input.GetCaseId() != "" {
		if _, err := tx.Exec(ctx, `UPDATE investigation_cases SET state='failed', updated_at=now()
			WHERE case_id=$1 AND state NOT IN ('finding_ready','shadow_observing','resolved')`, input.GetCaseId()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO case_activities(case_id, kind, ref_id, summary)
			VALUES($1,'run_progress',$2,'调查执行实例失败，正文未写入审计')`, input.GetCaseId(), runID)
		return err
	}
	receipt := &workerv1.InvestigationReceipt{
		EventId: input.GetTicket().GetEventId(), TicketDigest: input.GetTicketDigest(),
		Status: status, ErrorCode: errorCode, MessageDigest: auditPayloadDigest(message),
	}
	return insertInvestigationReceipt(ctx, tx, runID, workID, input, receipt)
}

func insertInvestigationReceipt(ctx context.Context, tx pgx.Tx, runID, workID string, input *workerv1.InvestigationInput, receipt *workerv1.InvestigationReceipt) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(receipt)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO investigation_receipts(
		run_id, work_id, event_id, ticket_digest, status, receipt, error_code)
		VALUES($1,$2,$3,$4,$5,$6::jsonb,$7) ON CONFLICT(run_id) DO NOTHING`,
		runID, workID, input.GetTicket().GetEventId(), input.GetTicketDigest(), receipt.GetStatus(), string(raw), receipt.GetErrorCode())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var storedStatus, storedDigest string
	var storedRaw []byte
	if err := tx.QueryRow(ctx, `SELECT status, ticket_digest, receipt FROM investigation_receipts WHERE run_id=$1`, runID).
		Scan(&storedStatus, &storedDigest, &storedRaw); err != nil {
		return err
	}
	var storedReceipt workerv1.InvestigationReceipt
	if err := protojson.Unmarshal(storedRaw, &storedReceipt); err != nil {
		return err
	}
	if storedStatus != receipt.GetStatus() || storedDigest != input.GetTicketDigest() || !proto.Equal(&storedReceipt, receipt) {
		return fmt.Errorf("investigation terminal receipt conflicts with %s", storedStatus)
	}
	return nil
}
