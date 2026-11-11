package settings

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/log"

	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/database"
)

// ownerChatIDKey persists the instance owner's chat ID once it has been
// captured. meta.* keys are skipped by settings.Load, so the value is pure
// bookkeeping and never a config group.
const ownerChatIDKey = "meta.owner_chat_id"

var (
	ownerMu     sync.Mutex
	ownerCached bool
	ownerChatID int64
)

// OwnerChatID returns the instance owner's chat ID, or 0 when it cannot be
// determined yet. The owner is captured exactly once — from the first
// whitelisted user at the moment of the first call (a v0 config import
// creates its users before this runs, so imported entries win) — and
// persisted under meta.owner_chat_id. Later whitelist changes never
// reassign the owner: a per-boot derivation such as "the lowest whitelisted
// chat id" would silently move ownership when the whitelist changes, which
// diverges from the v0 fork semantics this port preserves.
//
// Until a value is captured and stored, 0 is returned and callers must keep
// the gates backed by it closed.
func OwnerChatID(ctx context.Context) int64 {
	ownerMu.Lock()
	defer ownerMu.Unlock()
	if ownerCached {
		return ownerChatID
	}
	id := loadOwnerChatID(ctx)
	if id != 0 {
		ownerChatID, ownerCached = id, true
	}
	return ownerChatID
}

func loadOwnerChatID(ctx context.Context) int64 {
	value, ok, err := database.GetSetting(ctx, ownerChatIDKey)
	if err != nil {
		log.Errorf("Failed to read the persisted owner chat id: %v", err)
		return 0
	}
	if ok {
		if id, perr := strconv.ParseInt(strings.TrimSpace(value), 10, 64); perr == nil && id != 0 {
			return id
		}
		// A stored but unparsable value was never a valid capture; take a
		// fresh one rather than staying gated off forever.
		log.Warn("Persisted owner chat id is unparsable, capturing a new one", "value", value)
	}
	ids := config.C().GetUsersID()
	if len(ids) == 0 {
		return 0
	}
	owner := ids[0]
	if err := database.SetSetting(ctx, ownerChatIDKey, strconv.FormatInt(owner, 10)); err != nil {
		// Not cached: the next call retries the capture instead of the
		// instance staying ownerless for its whole lifetime.
		log.Errorf("Failed to persist the owner chat id: %v", err)
		return 0
	}
	return owner
}
