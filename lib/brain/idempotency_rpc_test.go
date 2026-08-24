package brain

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestIdempotentProtoRollsBackBusinessWhenResponseCannotCommit(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	if _, err := st.Pool().Exec(ctx, `ALTER TABLE idempotency_keys ADD CONSTRAINT reject_ok_for_test CHECK (status_code <> 'ok')`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := st.Pool().Exec(ctx, `ALTER TABLE idempotency_keys DROP CONSTRAINT reject_ok_for_test`); err != nil {
			t.Errorf("恢复幂等键表约束: %v", err)
		}
	}()
	err := idempotentProto(ctx, st.Pool(), "atomic-test", "key-1", &emptypb.Empty{}, &emptypb.Empty{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO users(user_id, username, display_name, role, state, password_hash)
			VALUES('atomic-user','atomic-user','','viewer','active','hash')`)
		return err
	})
	if err == nil {
		t.Fatal("idempotency completion failure must fail the write")
	}
	var users int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM users WHERE user_id='atomic-user'`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 0 {
		t.Fatal("business write committed without idempotency response")
	}
	var pending int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE scope='atomic-test' AND idem_key='key-1'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatal("rolled back write must not leave a pending key")
	}
}

func TestIdempotentProtoReplaysPersistedErrorWithoutRepeatingEffect(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	calls := 0
	run := func(tx pgx.Tx) error {
		calls++
		if _, err := tx.Exec(ctx, `INSERT INTO users(user_id, username, display_name, role, state, password_hash)
			VALUES('failed-state-user','failed-state-user','','viewer','active','hash')`); err != nil {
			return err
		}
		return persistRPCError(connect.NewError(connect.CodeFailedPrecondition, errors.New("probe failed")))
	}
	for i := 0; i < 2; i++ {
		err := idempotentProto(ctx, st.Pool(), "error-test", "key-2", &emptypb.Empty{}, &emptypb.Empty{}, run)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("attempt %d code=%s err=%v", i, connect.CodeOf(err), err)
		}
	}
	if calls != 1 {
		t.Fatalf("persisted failure executed %d times", calls)
	}
}

func TestReserveIdempotencyTakesOverExpiredPending(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	digest := requestDigest("scope", "body", "key-stale")
	hit, _, _, err := reserveIdempotency(ctx, st.Pool(), "stale-test", "key-stale", digest)
	if err != nil || hit {
		t.Fatalf("first reserve execute hit=%v err=%v", hit, err)
	}
	_, _, _, err = reserveIdempotency(ctx, st.Pool(), "stale-test", "key-stale", digest)
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("live pending want aborted, got %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE idempotency_keys SET created_at=now()-interval '3 minutes'
		WHERE scope='stale-test' AND idem_key='key-stale'`); err != nil {
		t.Fatal(err)
	}
	hit, _, _, err = reserveIdempotency(ctx, st.Pool(), "stale-test", "key-stale", digest)
	if err != nil || hit {
		t.Fatalf("expired pending must be takeable: hit=%v err=%v", hit, err)
	}
	other := requestDigest("scope", "other", "key-stale")
	if _, err := st.Pool().Exec(ctx, `UPDATE idempotency_keys SET created_at=now()-interval '3 minutes'
		WHERE scope='stale-test' AND idem_key='key-stale'`); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = reserveIdempotency(ctx, st.Pool(), "stale-test", "key-stale", other)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expired pending with different digest want failed_precondition, got %v", err)
	}
}
