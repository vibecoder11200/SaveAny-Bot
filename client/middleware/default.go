package middleware

import (
	"context"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/td/telegram"
	"github.com/krau/SaveAny-Bot/client/middleware/recovery"
	"github.com/krau/SaveAny-Bot/client/middleware/retry"
	"github.com/krau/SaveAny-Bot/config"
)

// https://github.com/iyear/tdl/blob/master/core/tclient/tclient.go
func NewDefaultMiddlewares(ctx context.Context, timeout time.Duration) []telegram.Middleware {
	return []telegram.Middleware{
		recovery.New(ctx, func() backoff.BackOff { return newBackoff(timeout) }),
		retry.New(config.C().Telegram.RpcRetry),
		// By default SimpleWaiter retries flood waits forever and silently,
		// which turns a long account-level FLOOD_WAIT (hours) into an
		// unexplainable hang. Give up instead: bounded retries, and any
		// single wait longer than maxFloodWait surfaces as an error.
		floodwait.NewSimpleWaiter().
			WithMaxRetries(maxFloodWaitRetries).
			WithMaxWait(maxFloodWait),
	}
}

const (
	maxFloodWaitRetries = 3
	maxFloodWait        = 5 * time.Minute
)

func newBackoff(timeout time.Duration) backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.Multiplier = 1.1
	b.MaxElapsedTime = timeout
	b.MaxInterval = 10 * time.Second
	return b
}
