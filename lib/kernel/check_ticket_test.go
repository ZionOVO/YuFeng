package kernel

import (
	"testing"

	eventv1 "yufeng/proto/gen/eventv1"
)

func TestCheckTicketDigestIsDeterministicAndRejectsNil(t *testing.T) {
	ticket := &eventv1.CheckTicket{EventId: "event", AssetId: "asset", Method: "GET"}
	first, err := CheckTicketDigest(ticket)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CheckTicketDigest(ticket)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != len("sha256:")+64 {
		t.Fatalf("unexpected digest %q %q", first, second)
	}
	if _, err := CheckTicketDigest(nil); err == nil {
		t.Fatal("nil ticket must be rejected")
	}
}
