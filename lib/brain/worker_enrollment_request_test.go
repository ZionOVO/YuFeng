package brain

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"connectrpc.com/connect"

	workerv1 "yufeng/proto/gen/workerv1"
)

func TestRequestWorkerEnrollmentReturnsPersistedStateOnReplay(t *testing.T) {
	st, ctx := openTestStore(t)
	defer st.Close()
	server := NewWorkerServer(st.Pool(), mustKey(t), false)

	for _, state := range []string{"pending", "approved", "denied"} {
		t.Run(state, func(t *testing.T) {
			workerID := "worker-enrollment-replay-" + state + "-" + newTestSuffix()
			certificateRequest, workerPublicKey := makeWorkerEnrollmentCertificateRequest(t, workerID)
			activationKey, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			request := &workerv1.RequestWorkerEnrollmentRequest{
				WorkerId: workerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
				WorkerPublicKey: workerPublicKey, OperatingSystem: "linux", Architecture: "amd64",
				CertificateRequest:  certificateRequest,
				ActivationPublicKey: base64.RawURLEncoding.EncodeToString(activationKey.PublicKey().Bytes()),
				Version:             "test-v1",
			}
			first, err := server.RequestWorkerEnrollment(ctx, connect.NewRequest(request))
			if err != nil {
				t.Fatal(err)
			}
			if first.Msg.GetState() != "pending" || first.Msg.GetEnrollmentId() == "" || first.Msg.GetPublicKeyFingerprint() == "" {
				t.Fatalf("initial enrollment response=%v", first.Msg)
			}
			if state != "pending" {
				if _, err := st.Pool().Exec(ctx, `UPDATE worker_enrollments SET state=$2 WHERE enrollment_id=$1`,
					first.Msg.GetEnrollmentId(), state); err != nil {
					t.Fatal(err)
				}
			}

			repeated, err := server.RequestWorkerEnrollment(ctx, connect.NewRequest(request))
			if err != nil {
				t.Fatal(err)
			}
			if repeated.Msg.GetEnrollmentId() != first.Msg.GetEnrollmentId() ||
				repeated.Msg.GetPublicKeyFingerprint() != first.Msg.GetPublicKeyFingerprint() ||
				repeated.Msg.GetState() != state {
				t.Fatalf("replayed enrollment response=%v, want id=%q fingerprint=%q state=%q",
					repeated.Msg, first.Msg.GetEnrollmentId(), first.Msg.GetPublicKeyFingerprint(), state)
			}
			var count int
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM worker_enrollments
				WHERE worker_id=$1 AND public_key_fingerprint=$2`, workerID, first.Msg.GetPublicKeyFingerprint()).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("replayed enrollment rows=%d, want 1", count)
			}

			if state == "pending" {
				changedKey, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				request.ActivationPublicKey = base64.RawURLEncoding.EncodeToString(changedKey.PublicKey().Bytes())
				if _, err := server.RequestWorkerEnrollment(ctx, connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeAlreadyExists {
					t.Fatalf("changed activation key error=%v, want already_exists", err)
				}
			}
		})
	}
}
