package brain

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	runv1 "yufeng/proto/gen/runv1"
)

type runBudgetAmount struct {
	Steps           int64
	ModelCalls      int64
	InputTokens     int64
	OutputTokens    int64
	ToolCalls       int64
	ToolResultBytes int64
	CostMicrounits  int64
}

type runBudgetAccount struct {
	BudgetID          string
	State             string
	Limit             runBudgetAmount
	Used              runBudgetAmount
	Reserved          runBudgetAmount
	MaxActiveMillis   int64
	ActiveMillisUsed  int64
	ActiveStartedAt   *time.Time
	ExecutionDeadline time.Time
}

func createRunBudgetAccount(ctx context.Context, db dbTX, budgetID, runID string, calls int64, ttl time.Duration, now time.Time) (time.Time, error) {
	if calls <= 0 || ttl <= 0 {
		return time.Time{}, errors.New("run budget limits are invalid")
	}
	deadline := now.Add(ttl)
	_, err := db.Exec(ctx, `INSERT INTO run_budget_accounts(
		budget_id, run_id, max_steps, max_model_calls, max_input_tokens, max_output_tokens,
		max_tool_calls, max_tool_result_bytes, max_cost_microunits, max_active_milliseconds, execution_deadline)
		VALUES($1,$2,$3,$3,$4,$5,$3,$6,$7,$8,$9)`, budgetID, runID, calls,
		calls*kernel.RunModelInputTokensPerCall, calls*kernel.ChatCompleteMaxTokens,
		calls*kernel.RunToolResultBytesPerCall, calls*kernel.RunModelCostMicrounitsPerCall, ttl.Milliseconds(), deadline)
	return deadline, err
}

func reserveRunBudget(ctx context.Context, db dbTX, budgetID, kind, requestKey string, amount runBudgetAmount) (string, error) {
	if budgetID == "" || requestKey == "" {
		return "", nil
	}
	if !validRunBudgetAmount(amount) {
		return "", errors.New("run budget reservation is invalid")
	}
	account, err := lockRunBudgetAccount(ctx, db, budgetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var existingID, state string
	var existing runBudgetAmount
	err = db.QueryRow(ctx, `SELECT reservation_id, state, steps, model_calls, input_tokens, output_tokens,
		tool_calls, tool_result_bytes, cost_microunits FROM run_budget_reservations
		WHERE budget_id=$1 AND kind=$2 AND request_key=$3 FOR UPDATE`, budgetID, kind, requestKey).
		Scan(&existingID, &state, &existing.Steps, &existing.ModelCalls, &existing.InputTokens,
			&existing.OutputTokens, &existing.ToolCalls, &existing.ToolResultBytes, &existing.CostMicrounits)
	if err == nil {
		if existing != amount {
			return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("run budget reservation changed"))
		}
		return existingID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	now := time.Now()
	active := account.currentActiveMillis(now)
	if account.State != "active" || !now.Before(account.ExecutionDeadline) || active >= account.MaxActiveMillis ||
		!runBudgetFits(account.Limit, addRunBudgetAmount(account.Used, account.Reserved), amount) {
		return "", connect.NewError(connect.CodeResourceExhausted, errors.New("run budget exhausted or expired"))
	}
	reservationID, err := newID("budgetres")
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(ctx, `INSERT INTO run_budget_reservations(
		reservation_id, budget_id, kind, request_key, steps, model_calls, input_tokens, output_tokens,
		tool_calls, tool_result_bytes, cost_microunits)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, reservationID, budgetID, kind, requestKey,
		amount.Steps, amount.ModelCalls, amount.InputTokens, amount.OutputTokens,
		amount.ToolCalls, amount.ToolResultBytes, amount.CostMicrounits); err != nil {
		return "", err
	}
	activeStarted := account.ActiveStartedAt
	if activeStarted != nil {
		activeStarted = &now
	}
	_, err = db.Exec(ctx, `UPDATE run_budget_accounts SET
		steps_reserved=steps_reserved+$2, model_calls_reserved=model_calls_reserved+$3,
		input_tokens_reserved=input_tokens_reserved+$4, output_tokens_reserved=output_tokens_reserved+$5,
		tool_calls_reserved=tool_calls_reserved+$6, tool_result_bytes_reserved=tool_result_bytes_reserved+$7,
		cost_microunits_reserved=cost_microunits_reserved+$8, active_milliseconds_used=$9,
		active_started_at=$10, updated_at=now() WHERE budget_id=$1`, budgetID, amount.Steps,
		amount.ModelCalls, amount.InputTokens, amount.OutputTokens, amount.ToolCalls,
		amount.ToolResultBytes, amount.CostMicrounits, active, activeStarted)
	if err != nil {
		return "", err
	}
	return reservationID, nil
}

func settleRunBudget(ctx context.Context, db dbTX, reservationID, state string, actual runBudgetAmount) error {
	if reservationID == "" {
		return nil
	}
	if state != "settled" && state != "outcome_unknown" {
		return errors.New("run budget settlement state is invalid")
	}
	if !validRunBudgetAmount(actual) {
		return errors.New("run budget settlement is invalid")
	}
	var budgetID string
	if err := db.QueryRow(ctx, `SELECT budget_id FROM run_budget_reservations WHERE reservation_id=$1`, reservationID).Scan(&budgetID); err != nil {
		return err
	}
	account, err := lockRunBudgetAccount(ctx, db, budgetID)
	if err != nil {
		return err
	}
	var reserved, storedActual runBudgetAmount
	var storedState string
	if err := db.QueryRow(ctx, `SELECT state, steps, model_calls, input_tokens, output_tokens, tool_calls,
		tool_result_bytes, cost_microunits, actual_steps, actual_model_calls, actual_input_tokens,
		actual_output_tokens, actual_tool_calls, actual_tool_result_bytes, actual_cost_microunits
		FROM run_budget_reservations WHERE reservation_id=$1 FOR UPDATE`, reservationID).
		Scan(&storedState, &reserved.Steps, &reserved.ModelCalls, &reserved.InputTokens,
			&reserved.OutputTokens, &reserved.ToolCalls, &reserved.ToolResultBytes, &reserved.CostMicrounits,
			&storedActual.Steps, &storedActual.ModelCalls, &storedActual.InputTokens, &storedActual.OutputTokens,
			&storedActual.ToolCalls, &storedActual.ToolResultBytes, &storedActual.CostMicrounits); err != nil {
		return err
	}
	if storedState != "reserved" {
		if storedState != state || storedActual != actual {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("run budget settlement changed"))
		}
		return nil
	}
	if !runBudgetAmountWithin(actual, reserved) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("run budget settlement exceeds reservation"))
	}
	now := time.Now()
	active := account.currentActiveMillis(now)
	activeStarted := account.ActiveStartedAt
	if activeStarted != nil {
		activeStarted = &now
	}
	if _, err := db.Exec(ctx, `UPDATE run_budget_accounts SET
		steps_reserved=steps_reserved-$2, steps_used=steps_used+$3,
		model_calls_reserved=model_calls_reserved-$4, model_calls_used=model_calls_used+$5,
		input_tokens_reserved=input_tokens_reserved-$6, input_tokens_used=input_tokens_used+$7,
		output_tokens_reserved=output_tokens_reserved-$8, output_tokens_used=output_tokens_used+$9,
		tool_calls_reserved=tool_calls_reserved-$10, tool_calls_used=tool_calls_used+$11,
		tool_result_bytes_reserved=tool_result_bytes_reserved-$12, tool_result_bytes_used=tool_result_bytes_used+$13,
		cost_microunits_reserved=cost_microunits_reserved-$14, cost_microunits_used=cost_microunits_used+$15,
		active_milliseconds_used=$16, active_started_at=$17, updated_at=now() WHERE budget_id=$1`, budgetID,
		reserved.Steps, actual.Steps, reserved.ModelCalls, actual.ModelCalls, reserved.InputTokens,
		actual.InputTokens, reserved.OutputTokens, actual.OutputTokens, reserved.ToolCalls, actual.ToolCalls,
		reserved.ToolResultBytes, actual.ToolResultBytes, reserved.CostMicrounits, actual.CostMicrounits,
		active, activeStarted); err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE run_budget_reservations SET state=$2, actual_steps=$3,
		actual_model_calls=$4, actual_input_tokens=$5, actual_output_tokens=$6, actual_tool_calls=$7,
		actual_tool_result_bytes=$8, actual_cost_microunits=$9, settled_at=now() WHERE reservation_id=$1`,
		reservationID, state, actual.Steps, actual.ModelCalls, actual.InputTokens, actual.OutputTokens,
		actual.ToolCalls, actual.ToolResultBytes, actual.CostMicrounits)
	return err
}

func settleRunBudgetFull(ctx context.Context, db dbTX, reservationID, state string) error {
	if reservationID == "" {
		return nil
	}
	var amount runBudgetAmount
	if err := db.QueryRow(ctx, `SELECT steps, model_calls, input_tokens, output_tokens, tool_calls,
		tool_result_bytes, cost_microunits FROM run_budget_reservations WHERE reservation_id=$1`, reservationID).
		Scan(&amount.Steps, &amount.ModelCalls, &amount.InputTokens, &amount.OutputTokens,
			&amount.ToolCalls, &amount.ToolResultBytes, &amount.CostMicrounits); err != nil {
		return err
	}
	return settleRunBudget(ctx, db, reservationID, state, amount)
}

func settleRunBudgetByKey(ctx context.Context, db dbTX, budgetID, kind, requestKey, state string, actual runBudgetAmount) error {
	if budgetID == "" {
		return nil
	}
	var reservationID string
	err := db.QueryRow(ctx, `SELECT reservation_id FROM run_budget_reservations
		WHERE budget_id=$1 AND kind=$2 AND request_key=$3`, budgetID, kind, requestKey).Scan(&reservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return settleRunBudget(ctx, db, reservationID, state, actual)
}

func closeRunBudget(ctx context.Context, db dbTX, budgetID, state string, requireSettled bool, now time.Time) error {
	if budgetID == "" {
		return nil
	}
	account, err := lockRunBudgetAccount(ctx, db, budgetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if requireSettled && account.Reserved != (runBudgetAmount{}) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("run has unsettled budget reservations"))
	}
	active := account.currentActiveMillis(now)
	if !requireSettled && account.Reserved != (runBudgetAmount{}) {
		if _, err := db.Exec(ctx, `UPDATE run_budget_reservations SET state='outcome_unknown',
			actual_steps=steps, actual_model_calls=model_calls, actual_input_tokens=input_tokens,
			actual_output_tokens=output_tokens, actual_tool_calls=tool_calls,
			actual_tool_result_bytes=tool_result_bytes, actual_cost_microunits=cost_microunits,
			settled_at=now() WHERE budget_id=$1 AND state='reserved'`, budgetID); err != nil {
			return err
		}
	}
	_, err = db.Exec(ctx, `UPDATE run_budget_accounts SET state=$2,
		steps_used=steps_used+steps_reserved, steps_reserved=0,
		model_calls_used=model_calls_used+model_calls_reserved, model_calls_reserved=0,
		input_tokens_used=input_tokens_used+input_tokens_reserved, input_tokens_reserved=0,
		output_tokens_used=output_tokens_used+output_tokens_reserved, output_tokens_reserved=0,
		tool_calls_used=tool_calls_used+tool_calls_reserved, tool_calls_reserved=0,
		tool_result_bytes_used=tool_result_bytes_used+tool_result_bytes_reserved, tool_result_bytes_reserved=0,
		cost_microunits_used=cost_microunits_used+cost_microunits_reserved, cost_microunits_reserved=0,
		active_milliseconds_used=$3, active_started_at=NULL, updated_at=now() WHERE budget_id=$1`,
		budgetID, state, active)
	return err
}

func startRunBudgetActive(ctx context.Context, db dbTX, budgetID string, now time.Time) error {
	account, err := lockRunBudgetAccount(ctx, db, budgetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	active := account.currentActiveMillis(now)
	if account.State != "active" || !now.Before(account.ExecutionDeadline) || active >= account.MaxActiveMillis {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("run budget exhausted or expired"))
	}
	_, err = db.Exec(ctx, `UPDATE run_budget_accounts SET active_milliseconds_used=$2,
		active_started_at=$3, updated_at=now() WHERE budget_id=$1`, budgetID, active, now)
	return err
}

func stopRunBudgetActive(ctx context.Context, db dbTX, budgetID string, now time.Time) error {
	account, err := lockRunBudgetAccount(ctx, db, budgetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	active := account.currentActiveMillis(now)
	_, err = db.Exec(ctx, `UPDATE run_budget_accounts SET active_milliseconds_used=$2,
		active_started_at=NULL, updated_at=now() WHERE budget_id=$1`, budgetID, active)
	return err
}

func loadRunBudgetSnapshot(ctx context.Context, db dbTX, budgetID string) (*runv1.RunBudgetSnapshot, error) {
	if budgetID == "" {
		return nil, nil
	}
	account, err := loadRunBudgetAccount(ctx, db, budgetID, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	account.ActiveMillisUsed = account.currentActiveMillis(time.Now())
	return account.snapshot(), nil
}

func loadRunBudgetForRun(ctx context.Context, pool *pgxpool.Pool, runID string) (*runv1.RunBudgetSnapshot, error) {
	var budgetID string
	if err := pool.QueryRow(ctx, `SELECT budget_id FROM run_budget_accounts WHERE run_id=$1`, runID).Scan(&budgetID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return loadRunBudgetSnapshot(ctx, pool, budgetID)
}

func lockRunBudgetAccount(ctx context.Context, db dbTX, budgetID string) (runBudgetAccount, error) {
	return loadRunBudgetAccount(ctx, db, budgetID, true)
}

func loadRunBudgetAccount(ctx context.Context, db dbTX, budgetID string, lock bool) (runBudgetAccount, error) {
	var account runBudgetAccount
	query := `SELECT budget_id, state,
		max_steps, max_model_calls, max_input_tokens, max_output_tokens, max_tool_calls, max_tool_result_bytes, max_cost_microunits,
		steps_used, model_calls_used, input_tokens_used, output_tokens_used, tool_calls_used, tool_result_bytes_used, cost_microunits_used,
		steps_reserved, model_calls_reserved, input_tokens_reserved, output_tokens_reserved, tool_calls_reserved, tool_result_bytes_reserved, cost_microunits_reserved,
		max_active_milliseconds, active_milliseconds_used, active_started_at, execution_deadline
		FROM run_budget_accounts WHERE budget_id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	err := db.QueryRow(ctx, query, budgetID).Scan(&account.BudgetID, &account.State,
		&account.Limit.Steps, &account.Limit.ModelCalls, &account.Limit.InputTokens, &account.Limit.OutputTokens,
		&account.Limit.ToolCalls, &account.Limit.ToolResultBytes, &account.Limit.CostMicrounits,
		&account.Used.Steps, &account.Used.ModelCalls, &account.Used.InputTokens, &account.Used.OutputTokens,
		&account.Used.ToolCalls, &account.Used.ToolResultBytes, &account.Used.CostMicrounits,
		&account.Reserved.Steps, &account.Reserved.ModelCalls, &account.Reserved.InputTokens, &account.Reserved.OutputTokens,
		&account.Reserved.ToolCalls, &account.Reserved.ToolResultBytes, &account.Reserved.CostMicrounits,
		&account.MaxActiveMillis, &account.ActiveMillisUsed, &account.ActiveStartedAt, &account.ExecutionDeadline)
	return account, err
}

func (a runBudgetAccount) currentActiveMillis(now time.Time) int64 {
	used := a.ActiveMillisUsed
	if a.ActiveStartedAt != nil && now.After(*a.ActiveStartedAt) {
		used += now.Sub(*a.ActiveStartedAt).Milliseconds()
	}
	return used
}

func (a runBudgetAccount) snapshot() *runv1.RunBudgetSnapshot {
	return &runv1.RunBudgetSnapshot{
		BudgetId: a.BudgetID,
		State:    a.State,
		Limits: &runv1.RunBudgetLimits{
			MaxSteps: a.Limit.Steps, MaxModelCalls: a.Limit.ModelCalls,
			MaxInputTokens: a.Limit.InputTokens, MaxOutputTokens: a.Limit.OutputTokens,
			MaxToolCalls: a.Limit.ToolCalls, MaxToolResultBytes: a.Limit.ToolResultBytes,
			MaxCostMicrounits: a.Limit.CostMicrounits, MaxActiveMilliseconds: a.MaxActiveMillis,
		},
		Usage: &runv1.RunBudgetUsage{
			StepsUsed: a.Used.Steps, StepsReserved: a.Reserved.Steps,
			ModelCallsUsed: a.Used.ModelCalls, ModelCallsReserved: a.Reserved.ModelCalls,
			InputTokensUsed: a.Used.InputTokens, InputTokensReserved: a.Reserved.InputTokens,
			OutputTokensUsed: a.Used.OutputTokens, OutputTokensReserved: a.Reserved.OutputTokens,
			ToolCallsUsed: a.Used.ToolCalls, ToolCallsReserved: a.Reserved.ToolCalls,
			ToolResultBytesUsed: a.Used.ToolResultBytes, ToolResultBytesReserved: a.Reserved.ToolResultBytes,
			CostMicrounitsUsed: a.Used.CostMicrounits, CostMicrounitsReserved: a.Reserved.CostMicrounits,
			ActiveMillisecondsUsed: a.ActiveMillisUsed,
		},
		ExecutionDeadline: timestamppb.New(a.ExecutionDeadline),
	}
}

func validRunBudgetAmount(amount runBudgetAmount) bool {
	return amount.Steps >= 0 && amount.ModelCalls >= 0 && amount.InputTokens >= 0 &&
		amount.OutputTokens >= 0 && amount.ToolCalls >= 0 && amount.ToolResultBytes >= 0 && amount.CostMicrounits >= 0
}

func addRunBudgetAmount(a, b runBudgetAmount) runBudgetAmount {
	return runBudgetAmount{
		Steps: a.Steps + b.Steps, ModelCalls: a.ModelCalls + b.ModelCalls,
		InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens,
		ToolCalls: a.ToolCalls + b.ToolCalls, ToolResultBytes: a.ToolResultBytes + b.ToolResultBytes,
		CostMicrounits: a.CostMicrounits + b.CostMicrounits,
	}
}

func runBudgetFits(limit, consumed, requested runBudgetAmount) bool {
	next := addRunBudgetAmount(consumed, requested)
	return next.Steps <= limit.Steps && next.ModelCalls <= limit.ModelCalls &&
		next.InputTokens <= limit.InputTokens && next.OutputTokens <= limit.OutputTokens &&
		next.ToolCalls <= limit.ToolCalls && next.ToolResultBytes <= limit.ToolResultBytes &&
		next.CostMicrounits <= limit.CostMicrounits
}

func runBudgetAmountWithin(actual, reserved runBudgetAmount) bool {
	return actual.Steps <= reserved.Steps && actual.ModelCalls <= reserved.ModelCalls &&
		actual.InputTokens <= reserved.InputTokens && actual.OutputTokens <= reserved.OutputTokens &&
		actual.ToolCalls <= reserved.ToolCalls && actual.ToolResultBytes <= reserved.ToolResultBytes &&
		actual.CostMicrounits <= reserved.CostMicrounits
}
