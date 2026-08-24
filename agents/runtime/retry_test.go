package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegisterWithBackoffEventuallySucceeds(t *testing.T) {
	n := 0
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := registerWithBackoff(ctx, time.Millisecond, 5*time.Millisecond, func(context.Context) error {
		n++
		if n < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("tries=%d", n)
	}
}

func TestRegisterWithBackoffHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := RegisterWithBackoff(ctx, func(context.Context) error { return errors.New("x") })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
