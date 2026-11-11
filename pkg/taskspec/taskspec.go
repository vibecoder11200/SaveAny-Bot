// Package taskspec holds the serialized creation parameters of tasks. They
// are stored with the task record so a queued task can be rebuilt after a
// restart.
package taskspec

import (
	"time"

	"github.com/krau/SaveAny-Bot/pkg/parser"
)

// Version is the current persisted task-parameter schema.
const Version = 1

// TGFileItem identifies one Telegram file and its destination.
type TGFileItem struct {
	ID        string `json:"id,omitempty"`
	ChatID    int64  `json:"chat_id"`
	MessageID int    `json:"message_id"`
	Name      string `json:"name"`
	Storage   string `json:"storage"`
	StorageID uint   `json:"storage_id,omitempty"`
	Path      string `json:"path"`
}

// TGFile is used by both single-file and batch Telegram tasks.
type TGFile struct {
	Items     []TGFileItem `json:"items"`
	Overwrite bool         `json:"overwrite,omitempty"`
	// Done lists task element IDs whose upload already completed.
	Done []string `json:"done,omitempty"`
	// UserID is the users.id of the creating user; it scopes content dedup.
	// 0 = created without a user context (web API): dedup stays off for the
	// task, including after a resume rebuild.
	UserID uint `json:"user_id,omitempty"`
}

// UserID scopes content dedup for a task; the field comment on TGFile
// applies to every spec below that carries it.
type DirectLinks struct {
	UserID uint     `json:"user_id,omitempty"`
	URLs   []string `json:"urls"`
	// Path is the destination directory. A pointer distinguishes "root" from
	// an old record that has no persisted path.
	Path *string `json:"path,omitempty"`
}

type YTDLP struct {
	UserID uint     `json:"user_id,omitempty"`
	URLs   []string `json:"urls"`
	Flags  []string `json:"flags,omitempty"`
}

type Aria2 struct {
	GID    string   `json:"gid,omitempty"`
	UserID uint     `json:"user_id,omitempty"`
	URIs   []string `json:"uris"`
}

type Parsed struct {
	UserID uint         `json:"user_id,omitempty"`
	Item   *parser.Item `json:"item"`
}

type Telegraph struct {
	PhPath string   `json:"ph_path"`
	Pics   []string `json:"pics"`
	UserID uint     `json:"user_id,omitempty"`
}

// TransferItem describes one file of a storage-to-storage transfer.
type TransferItem struct {
	SourceStorage   string    `json:"source_storage"`
	SourceStorageID uint      `json:"source_storage_id,omitempty"`
	SourcePath      string    `json:"source_path"`
	TargetStorage   string    `json:"target_storage"`
	TargetStorageID uint      `json:"target_storage_id,omitempty"`
	TargetPath      string    `json:"target_path"`
	Name            string    `json:"name"`
	Size            int64     `json:"size"`
	ModTime         time.Time `json:"mod_time"`
}

type Transfer struct {
	UserID uint           `json:"user_id,omitempty"`
	Items  []TransferItem `json:"items"`
}
