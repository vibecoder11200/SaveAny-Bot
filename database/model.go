package database

import (
	"time"

	"gorm.io/gorm"
)

// PendingTask journals an in-flight TG-file task so it can be recovered
// after a restart. One row per task: written before the task enters the
// queue, deleted when the task truly finishes (success, failure, or user
// cancel) — a row that survives a crash is exactly the recovery input.
type PendingTask struct {
	ID        string    `gorm:"primaryKey"` // the task's xid
	UserID    int64     // requesting user's chat ID; 0 = created without a user context (HTTP API)
	Payload   string    // JSON journal.TGTaskPayload
	Attempts  int       // infrastructure-failure counter across recoveries; observational only
	CreatedAt time.Time // basis for the age-based drop in recovery
}

type User struct {
	gorm.Model
	ChatID           int64 `gorm:"uniqueIndex;not null"`
	Silent           bool
	DefaultStorage   string
	DefaultDir       uint // Dir.ID
	Dirs             []Dir
	ApplyRule        bool
	Rules            []Rule
	WatchChats       []WatchChat
	WatchNotify      bool
	FilenameStrategy string
	FilenameTemplate string
	ConflictStrategy string
	MediaFilter      string // comma-separated media types to accept; empty = accept all
}

type WatchChat struct {
	gorm.Model
	// (UserID, ChatID) is unique among live rows, enforced by the partial
	// index idx_watch_chats_user_chat_active (EnsureWatchChatUniqueIndex) —
	// a GORM tag cannot express its WHERE deleted_at IS NULL predicate, and
	// a full unique index would collide with soft-deleted rows.
	UserID    uint // User's database ID (not chat ID)
	ChatID    int64
	Filter    string
	MediaType string // comma-separated media types to accept; empty = no media type filtering
	// Per-watch destination override (fork): when either is set, the rule
	// layer is skipped for this watch and saves go to this storage/dir.
	StorageName string
	DirPath     string
	// LastMessageID is the catch-up cursor: the newest message ID already
	// observed (live) or durably enqueued (backfill) for this watch.
	// NULL/0 = never initialized.
	LastMessageID *int64
}

type Dir struct {
	gorm.Model
	UserID      uint
	StorageName string
	Path        string
}

type Rule struct {
	gorm.Model
	UserID      uint
	Type        string
	Data        string
	StorageName string
	DirPath     string
}
