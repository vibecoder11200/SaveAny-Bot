package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/krau/mygotg/ext"
)

// mygotg's ext.NewContext seeds its per-copy rand source from
// time.Now().UnixNano() (a WALL-clock value — note that time.Time.Equal
// compares monotonic readings instead, so it cannot guard this). The stream
// feeds Telegram's RandomID send nonces, so two copies sharing a seed that
// both send a message collide and Telegram silently drops the second send
// as a duplicate. Windows clock granularity makes same-tick pairs realistic
// under the boot catch-up burst, which creates one context per task
// notification in a tight loop.
//
// newExtContext serializes context copies and makes every copy's seed
// strictly greater than the previous one: it records the wall-clock reading
// AFTER the constructor returns (the seed was read at or below that
// reading) and the next copy waits until the clock has moved past it. The
// wait is bounded — after maxSeedWaitTicks attempts it proceeds (a
// collision is then possible but a frozen clock must not hold the mutex
// forever). Copies are rare (one per update or task), so the mutex and the
// sub-millisecond wait cost nothing.
var (
	extCopyMu     sync.Mutex
	lastSeedAfter int64
)

const maxSeedWaitTicks = 10000

func newExtContext(base context.Context, bot *ext.Context, entities *tg.Entities) *ext.Context {
	extCopyMu.Lock()
	defer extCopyMu.Unlock()
	for ticks := 0; time.Now().UnixNano() <= lastSeedAfter && ticks < maxSeedWaitTicks; ticks++ {
		time.Sleep(100 * time.Microsecond)
	}
	ctx := ext.NewContext(base, bot.Raw, bot.PeerStorage, bot.Self, bot.Sender, entities, false)
	// Record after the constructor: the seed it read is at or below this
	// reading, so any strictly later copy seeds strictly higher.
	lastSeedAfter = time.Now().UnixNano()
	return ctx
}
