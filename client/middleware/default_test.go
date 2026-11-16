package middleware

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tgerr"

	"github.com/krau/SaveAny-Bot/common/utils/dlutil"
)

// The default chain must cap the floodwait sleeper at MaxAutoFloodWait: an
// oversized wait is refused immediately with the honest cause instead of
// parking the RPC past the download stall watchdog and dying mislabeled as
// a stall. Pins the WithMaxWait wiring, not just the constants.
func TestDefaultChainCapsFloodWaitSleep(t *testing.T) {
	var waiter *floodwait.SimpleWaiter
	for _, m := range NewDefaultMiddlewares(context.Background(), time.Minute) {
		if w, ok := m.(*floodwait.SimpleWaiter); ok {
			waiter = w
		}
	}
	if waiter == nil {
		t.Fatal("no floodwait waiter in the default middleware chain")
	}
	over := int((dlutil.MaxAutoFloodWait + time.Minute).Seconds())
	stub := tgInvokerFunc(func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		return tgerr.New(420, fmt.Sprintf("FLOOD_WAIT_%d", over))
	})
	invoke := waiter.Handle(stub)
	start := time.Now()
	err := invoke(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("oversized flood wait must be refused")
	}
	if !strings.Contains(err.Error(), "too big") {
		t.Fatalf("refusal must name the cause, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("refusal took %s — the waiter slept instead of refusing", elapsed)
	}
}

type tgInvokerFunc func(ctx context.Context, input bin.Encoder, output bin.Decoder) error

func (f tgInvokerFunc) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return f(ctx, input, output)
}
