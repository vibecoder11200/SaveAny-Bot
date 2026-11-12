package handlers

import (
	"context"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/krau/mygotg/ext"
)

// mygotg's ext.NewContext seeds its per-copy rand source from
// time.Now().UnixNano(). Two copies created in the same clock tick share
// the seed — and the stream feeds Telegram's RandomID send nonces, so a
// pair of same-seed copies that both send a message collides and Telegram
// silently drops the second send as a duplicate. Windows clock granularity
// makes same-tick pairs realistic under the boot catch-up burst, which
// creates one context per task notification in a tight loop.
//
// newExtContext serializes context copies and waits for the clock to
// advance past the previous copy's creation instant, so every copy this
// process builds seeds differently. Copies are rare (one per update or
// task), so the mutex and the rare sub-millisecond wait cost nothing.
var (
	extCopyMu       sync.Mutex
	lastExtCopyTime time.Time
)

func newExtContext(base context.Context, bot *ext.Context, entities *tg.Entities) *ext.Context {
	extCopyMu.Lock()
	defer extCopyMu.Unlock()
	for time.Now().Equal(lastExtCopyTime) {
		time.Sleep(100 * time.Microsecond)
	}
	lastExtCopyTime = time.Now()
	return ext.NewContext(base, bot.Raw, bot.PeerStorage, bot.Self, bot.Sender, entities, false)
}
