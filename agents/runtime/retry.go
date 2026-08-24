package runtime

import (
	"context"
	"time"
)

// RegisterWithBackoff 按指数退避重试 fn，直到成功或 ctx 取消。
func RegisterWithBackoff(ctx context.Context, fn func(context.Context) error) error {
	return registerWithBackoff(ctx, 200*time.Millisecond, 30*time.Second, fn)
}

func registerWithBackoff(ctx context.Context, wait, maxWait time.Duration, fn func(context.Context) error) error {
	for {
		if err := fn(ctx); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
		if wait > maxWait {
			wait = maxWait
		}
	}
}
