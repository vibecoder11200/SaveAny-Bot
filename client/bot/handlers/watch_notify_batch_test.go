package handlers

import (
	"context"
	"testing"

	"github.com/celestix/gotgproto/ext"
	"github.com/krau/SaveAny-Bot/core/tasks/batchtfile"
	"github.com/krau/SaveAny-Bot/database"
)

// Notifications disabled (or no bot context) must yield a nil tracker: the
// batch executor nil-checks its progress on every callback, so nil is how
// "do not notify" is expressed.
func TestNewBatchWatchNotifyProgressDisabled(t *testing.T) {
	botCtx := ext.NewContext(context.Background(), nil, nil, nil, nil, nil, false)
	if got := newBatchWatchNotifyProgress(context.Background(), botCtx, &database.User{ChatID: 42, WatchNotify: false}, nil, nil); got != nil {
		t.Fatal("WatchNotify=false must return nil")
	}
	if got := newBatchWatchNotifyProgress(context.Background(), nil, &database.User{ChatID: 42, WatchNotify: true}, nil, nil); got != nil {
		t.Fatal("nil botCtx must return nil")
	}
}

// stubTaskInfo is the minimum batchtfile.TaskInfo for render-side helpers.
type stubTaskInfo struct {
	items []batchtfile.TaskItemProgress
}

func (s stubTaskInfo) TaskID() string                           { return "stub" }
func (s stubTaskInfo) TotalSize() int64                         { return 0 }
func (s stubTaskInfo) Downloaded() int64                        { return 0 }
func (s stubTaskInfo) ActualTotalSize() int64                   { return 0 }
func (s stubTaskInfo) Count() int                               { return len(s.items) }
func (s stubTaskInfo) Processing() []batchtfile.TaskElementInfo { return nil }
func (s stubTaskInfo) Items() []batchtfile.TaskItemProgress     { return s.items }

// The album notification names the album after its first named member,
// mirroring the album folder naming; an all-unnamed batch degrades to "".
func TestFirstBatchFileName(t *testing.T) {
	info := stubTaskInfo{items: []batchtfile.TaskItemProgress{
		{Name: ""}, {Name: "b.mp4"}, {Name: "c.jpg"},
	}}
	if got := firstBatchFileName(info); got != "b.mp4" {
		t.Fatalf("firstBatchFileName = %q, want b.mp4 (first non-empty)", got)
	}
	if got := firstBatchFileName(stubTaskInfo{}); got != "" {
		t.Fatalf("firstBatchFileName on empty batch = %q, want \"\"", got)
	}
}
