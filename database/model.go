package database

import (
	"time"

	"gorm.io/gorm"

	"github.com/krau/SaveAny-Bot/pkg/enums/storagefilter"
)

type User struct {
	gorm.Model
	ChatID               int64 `gorm:"uniqueIndex;not null"`
	Silent               bool
	DefaultStorageID     uint // StorageRecord.ID
	DefaultDirID         uint // Dir.ID
	Dirs                 []Dir
	ApplyRule            bool
	Rules                []Rule
	WatchChats           []WatchChat
	WatchNotify          bool
	FilenameStrategy     string
	FilenameTemplate     string
	ConflictStrategy     string
	MediaFilter          string             // comma-separated media types to accept; empty = accept all
	Enabled              bool               `gorm:"not null;default:true"`
	StorageFilterIDsJSON string             `gorm:"column:storage_filter_ids;type:text"` // JSON array of storage IDs
	StorageFilterMode    storagefilter.Mode `gorm:"size:16;not null;default:blacklist"`  // blacklist or whitelist
}

type WatchChat struct {
	gorm.Model
	// (UserID, ChatID) is unique among live rows, enforced by the partial
	// index idx_watch_chats_user_chat_active (EnsureWatchChatUniqueIndex) —
	// a GORM tag cannot express its WHERE deleted_at IS NULL predicate, and
	// a full unique index would collide with soft-deleted rows
	// (watch→unwatch→watch-again).
	UserID        uint
	ChatID        int64
	MessageFilter string // filter type and expression, for example msgre:foo
	// MediaType accepts a comma-separated list (photo, video, document,
	// audio); empty = no media type filtering.
	MediaType string
	// StorageID is the per-watch destination override: 0 = inherit (rule
	// layer decides); non-zero skips the rule layer for this watch.
	StorageID uint
	// DirPath is the per-watch directory override (relative to the storage
	// root); empty = rule layer decides.
	DirPath string
	// LastMessageID is the catch-up cursor: the newest message ID already
	// observed (live) or durably enqueued (backfill) for this watch.
	// NULL/0 = never initialized.
	LastMessageID *int64
}

// SavedFileDedup indexes saved file content per (user, storage): one row per
// file whose Telegram file id or sha256 content hash was recorded at save
// time. Unlike the path-based telegram_saved_files ledger, it survives
// renames and name collisions, so a re-save of identical content can be
// blocked before any download or upload happens.
//
// Hard rows without gorm.Model: dedup entries are facts about saved content,
// not soft-deletable user records. Uniqueness is enforced by the partial
// unique indexes created in migration 2 (gorm tags cannot express the
// non-empty WHERE clauses that keep resends and non-TG files — both carrying
// empty-string keys — from colliding).
type SavedFileDedup struct {
	ID           uint   `gorm:"primaryKey"`
	UserID       uint   // User.ID
	StorageID    uint   // StorageRecord.ID
	FileUniqueID string // "" when the source is not Telegram media (or a resend: no hash either)
	ContentHash  string // "" for resends: the bytes never touched this disk
	Size         int64
	StoragePath  string
	ChatID       int64 // source chat for /history; 0 = unknown (legacy rows and non-TG sources)
	MessageID    int64
	CreatedAt    time.Time
}

func (SavedFileDedup) TableName() string { return "saved_file_dedups" }

// TelegramSavedFile indexes one file this bot has saved through a telegram
// storage. Telegram offers no way to ask whether a file already exists in a
// chat, so conflict detection and skip-on-recovery for that backend ride on
// this ledger instead. Files saved before the ledger existed, or placed into
// the chat by hand, are invisible to it.
type TelegramSavedFile struct {
	ID          uint   `gorm:"primaryKey"`
	StorageID   uint   `gorm:"uniqueIndex:idx_telegram_saved_files"` // StorageRecord.ID
	StoragePath string `gorm:"uniqueIndex:idx_telegram_saved_files"`
	CreatedAt   time.Time
}

func (TelegramSavedFile) TableName() string { return "telegram_saved_files" }

type Dir struct {
	gorm.Model
	UserID    uint   `gorm:"uniqueIndex:idx_dir_user_storage_path"`
	StorageID uint   `gorm:"uniqueIndex:idx_dir_user_storage_path"` // StorageRecord.ID
	Path      string `gorm:"uniqueIndex:idx_dir_user_storage_path"`
}

type Rule struct {
	gorm.Model
	UserID    uint
	Type      string
	Content   string // rule content interpreted according to Type
	StorageID uint   // StorageRecord.ID
	DirPath   string
}

type Setting struct {
	Key       string `gorm:"primaryKey;size:64"`
	Value     string `gorm:"type:text;not null"`
	UpdatedAt time.Time
}

type StorageRecord struct {
	gorm.Model
	Name          string `gorm:"uniqueIndex;size:64;not null"`
	Type          string `gorm:"size:32;not null"`
	Enable        bool   `gorm:"not null"`
	ConfigJSON    string `gorm:"column:config;type:text;not null"` // JSON storage configuration
	ConfigVersion int    `gorm:"not null;default:1"`
}

func (StorageRecord) TableName() string { return "storages" }

// BotWorker is an optional helper bot used for Telegram storage uploads.
type BotWorker struct {
	gorm.Model
	Name        string `gorm:"uniqueIndex;size:64;not null"`
	Token       string `gorm:"type:text;not null"`
	SessionPath string `gorm:"size:255"`
	Enabled     bool   `gorm:"not null"`
}

type TaskRecord struct {
	ID            uint   `gorm:"primaryKey"`
	TaskID        string `gorm:"uniqueIndex;size:64;not null"`
	Type          string `gorm:"size:32;not null"`
	Title         string
	Status        string `gorm:"size:16;index;not null"`
	StorageName   string
	StorageID     uint
	Path          string
	Source        string `gorm:"size:16;not null"`
	Error         string `gorm:"type:text"`
	Params        string `gorm:"type:text"`
	ParamsVersion int    `gorm:"not null;default:1"`
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	UpdatedAt     time.Time
}

// TaskItemRecord is the persisted outcome of one file inside a task. It keeps
// failed items addressable after the task leaves memory, so they can be
// reported and retried.
type TaskItemRecord struct {
	ID           uint   `gorm:"primaryKey"`
	TaskID       string `gorm:"index:idx_task_item_task_index,unique;size:64;not null"`
	Index        int    `gorm:"index:idx_task_item_task_index,unique;not null"`
	Name         string
	Path         string
	StorageName  string
	StorageID    uint
	Status       string `gorm:"size:16;index;not null"`
	Error        string `gorm:"type:text"`
	Size         int64
	SourceChatID int64
	SourceMsgID  int
}

func (TaskItemRecord) TableName() string {
	return "task_items"
}

type WebSession struct {
	ID        string `gorm:"primaryKey;size:64"`
	CSRF      string `gorm:"size:64;not null"`
	CreatedAt time.Time
	ExpiresAt time.Time `gorm:"index"`
}

type TaskFilter struct {
	Status string
	Type   string
	Limit  int
	Offset int
}

// TaskItemMetadata stores the normalized Telegram message metadata for one
// saved file. It is separate from TaskItemRecord so task lists do not load
// potentially large JSON payloads.
type TaskItemMetadata struct {
	ID              uint   `gorm:"primaryKey"`
	TaskID          string `gorm:"uniqueIndex:idx_task_metadata_item;size:64;not null"`
	ItemIndex       int    `gorm:"uniqueIndex:idx_task_metadata_item;not null"`
	Version         int    `gorm:"not null;default:1"`
	SourceChatID    int64  `gorm:"index"`
	SourceMessageID int    `gorm:"index"`
	JSON            string `gorm:"column:metadata;type:text;not null"`
	CreatedAt       time.Time
}

func (TaskItemMetadata) TableName() string {
	return "task_item_metadata"
}
