package handlers

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
	"github.com/krau/SaveAny-Bot/database"
	"github.com/krau/SaveAny-Bot/pkg/tcbdata"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

func TestParseWatchArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		chat        string
		msgRe       string
		mediaType   string
		storageName string
		dirPath     string
		wantErr     error
	}{
		{
			name: "chat only",
			args: []string{"-100123"},
			chat: "-100123",
		},
		{
			name:  "chat and username",
			args:  []string{"@channel"},
			chat:  "@channel",
			msgRe: "",
		},
		{
			name:  "regex containing colon survives",
			args:  []string{"chat", "msgre:(?i)^(hd|4k):"},
			chat:  "chat",
			msgRe: "(?i)^(hd|4k):",
		},
		{
			name:  "multi-word regex joins with spaces",
			args:  []string{"chat", "msgre:foo", "bar", "baz"},
			chat:  "chat",
			msgRe: "foo bar baz",
		},
		{
			name:      "multi-word regex stops at next flag",
			args:      []string{"chat", "msgre:foo", "bar", "mediatype:video"},
			chat:      "chat",
			msgRe:     "foo bar",
			mediaType: "video",
		},
		{
			name:      "flags in any order",
			args:      []string{"mediatype:photo,audio", "chat", "msgre:abc"},
			chat:      "chat",
			msgRe:     "abc",
			mediaType: "photo,audio",
		},
		{
			name:      "mediatype only",
			args:      []string{"chat", "mediatype:video"},
			chat:      "chat",
			mediaType: "video",
		},
		{
			name:    "unknown flag rejected",
			args:    []string{"chat", "floogle:foo"},
			chat:    "chat",
			wantErr: errWatchFilterTypeUnsupported,
		},
		{
			name:        "storage and dir flags parse as single tokens",
			args:        []string{"chat", "storage:local1", "dir:a/b/c", "msgre:x:y"},
			chat:        "chat",
			msgRe:       "x:y",
			storageName: "local1",
			dirPath:     "a/b/c",
		},
		{
			name:    "stray positional rejected",
			args:    []string{"chat", "foo", "bar"},
			chat:    "chat",
			wantErr: errWatchFilterFormatInvalid,
		},
		{
			name:  "no chat",
			args:  []string{"msgre:abc"},
			msgRe: "abc",
		},
		{
			name:  "msgre with empty value",
			args:  []string{"chat", "msgre:"},
			chat:  "chat",
			msgRe: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := parseWatchArgs(tt.args)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("parseWatchArgs() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWatchArgs() unexpected error: %v", err)
			}
			if parsed.Chat != tt.chat {
				t.Errorf("Chat = %q, want %q", parsed.Chat, tt.chat)
			}
			if parsed.MsgRe != tt.msgRe {
				t.Errorf("MsgRe = %q, want %q", parsed.MsgRe, tt.msgRe)
			}
			if parsed.MediaType != tt.mediaType {
				t.Errorf("MediaType = %q, want %q", parsed.MediaType, tt.mediaType)
			}
			if parsed.StorageName != tt.storageName {
				t.Errorf("StorageName = %q, want %q", parsed.StorageName, tt.storageName)
			}
			if parsed.DirPath != tt.dirPath {
				t.Errorf("DirPath = %q, want %q", parsed.DirPath, tt.dirPath)
			}
		})
	}
}

// A stored filter round-trips through the match-time accessor: the whole
// remainder after "msgre:" is the regex, colons included.
func TestWatchFilterRegexRoundTrip(t *testing.T) {
	const stored = "msgre:(?i)^(hd|4k):\\s*"
	regex, ok := watchFilterRegex(stored)
	if !ok {
		t.Fatal("watchFilterRegex() not ok for a valid stored filter")
	}
	if !regex.MatchString("HD: 2160p pack") {
		t.Error("regex should match a caption with the colon form")
	}
	if regex.MatchString("SD: 1080p pack") {
		t.Error("regex should not match a non-hd caption")
	}
	// The cache returns the same compiled instance.
	again, ok := watchFilterRegex(stored)
	if !ok || again != regex {
		t.Error("watchFilterRegex() should return the cached compiled regex")
	}
}

func TestWatchFilterRegexRejects(t *testing.T) {
	if _, ok := watchFilterRegex("mediatype:video"); ok {
		t.Error("non-msgre filter should not compile")
	}
	if _, ok := watchFilterRegex("msgre:([unclosed"); ok {
		t.Error("invalid regex should not compile")
	}
}

// The stored form must be reconstructible from the parse result exactly as
// handleWatchCmd does it.
func TestStoredFilterForm(t *testing.T) {
	parsed, err := parseWatchArgs([]string{"chat", "msgre:a:b c"})
	if err != nil {
		t.Fatal(err)
	}
	stored := watchFlagMsgRe + parsed.MsgRe
	if stored != "msgre:a:b c" {
		t.Errorf("stored = %q, want %q", stored, "msgre:a:b c")
	}
	if !strings.HasPrefix(stored, watchFlagMsgRe) {
		t.Fatal("stored filter must carry the msgre prefix")
	}
	regex, ok := watchFilterRegex(stored)
	if !ok {
		t.Fatal("stored filter must compile")
	}
	var _ *regexp.Regexp = regex
}

func TestResolveWatchTarget(t *testing.T) {
	tests := []struct {
		name string
		// inputs: watch override, rule result, user defaults
		watchStorage   string
		watchDir       string
		ruleStorage    string
		ruleDir        string
		defaultStorage string
		defaultDir     string
		want           watchTargetSpec
	}{
		{
			name:           "no override, no rule: user defaults",
			defaultStorage: "local1", defaultDir: "downloads",
			want: watchTargetSpec{StorageName: "local1", DirPath: "downloads"},
		},
		{
			name:         "override beats rule and defaults",
			watchStorage: "local2", watchDir: "wtest",
			ruleStorage: "local1", ruleDir: "ruledir",
			defaultStorage: "local1", defaultDir: "downloads",
			want: watchTargetSpec{StorageName: "local2", DirPath: "wtest"},
		},
		{
			name:        "dir-only override keeps default storage, drops rule",
			watchDir:    "wtest",
			ruleStorage: "local1", ruleDir: "ruledir",
			defaultStorage: "local1", defaultDir: "downloads",
			want: watchTargetSpec{StorageName: "local1", DirPath: "wtest"},
		},
		{
			// A storage-only override targets that storage's root: the user
			// default dir is a path on a DIFFERENT storage and must not be
			// transplanted.
			name:           "storage-only override targets the storage root",
			watchStorage:   "local2",
			defaultStorage: "local1", defaultDir: "downloads",
			want: watchTargetSpec{StorageName: "local2", DirPath: ""},
		},
		{
			name:        "rule beats defaults when no override",
			ruleStorage: "local3", ruleDir: "ruledir",
			defaultStorage: "local1", defaultDir: "downloads",
			want: watchTargetSpec{StorageName: "local3", DirPath: "ruledir"},
		},
		{
			name:           "rule storage only: default dir kept",
			ruleStorage:    "local3",
			defaultStorage: "local1", defaultDir: "downloads",
			want: watchTargetSpec{StorageName: "local3", DirPath: "downloads"},
		},
		{
			name:           "rule dir only: default storage kept",
			ruleDir:        "ruledir",
			defaultStorage: "local1", defaultDir: "downloads",
			want: watchTargetSpec{StorageName: "local1", DirPath: "ruledir"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveWatchTarget(tt.watchStorage, tt.watchDir, tt.ruleStorage, tt.ruleDir, tt.defaultStorage, tt.defaultDir)
			if got != tt.want {
				t.Errorf("resolveWatchTarget() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// buildGroupFile builds a grouped test file with caption and name for the
// group-filter tests.
func buildGroupFile(t *testing.T, caption, fileName string, groupID int64) tfile.TGFileMessage {
	t.Helper()
	doc := &tg.Document{
		ID:         1,
		Size:       10,
		MimeType:   "video/mp4",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: fileName}},
	}
	msg := &tg.Message{Message: caption, ID: 555}
	msg.Flags.Set(9)
	msg.Media = &tg.MessageMediaDocument{Document: doc}
	msg.SetGroupedID(groupID)
	file, err := tfile.FromMediaMessage(msg.Media, nil, msg)
	if err != nil {
		t.Fatalf("FromMediaMessage: %v", err)
	}
	return file
}

// Album filtering is per GroupedID: only ONE member carries the caption, so
// the group decision comes from that member; a filename-only regex still
// passes because the decision string appends every filename (/save parity).
func TestGroupMatchesFilter(t *testing.T) {
	captionOnFirst := []tfile.TGFileMessage{
		buildGroupFile(t, "hd: 2160p pack", "part1.jpg", 42),
		buildGroupFile(t, "", "part2.jpg", 42),
	}
	captionOnSecond := []tfile.TGFileMessage{
		buildGroupFile(t, "", "part1.jpg", 42),
		buildGroupFile(t, "4k: remux", "part2.jpg", 42),
	}
	noCaption := []tfile.TGFileMessage{
		buildGroupFile(t, "", "movie.mkv", 42),
		buildGroupFile(t, "", "movie2.mkv", 42),
	}
	re := regexp.MustCompile(`^(hd|4k):`)
	if !groupMatchesFilter(re, captionOnFirst) {
		t.Error("caption member decides: hd: caption should pass")
	}
	if !groupMatchesFilter(re, captionOnSecond) {
		t.Error("caption member (2nd) decides: 4k: caption should pass")
	}
	if groupMatchesFilter(re, noCaption) {
		t.Error("no caption: filename-only string must not match a caption regex")
	}
	// Filename parity: /save matches caption + " " + filename, so a
	// filename term matches even without any caption.
	fnRe := regexp.MustCompile(`movie\.mkv`)
	if !groupMatchesFilter(fnRe, noCaption) {
		t.Error("filename-only regex should match via the /save-parity string")
	}
}

func TestWatchConflictStrategyPin(t *testing.T) {
	// The pin (backfill) always wins.
	if got := watchConflictStrategy(nil, tcbdata.ConflictStrategyRename); got != tcbdata.ConflictStrategyRename {
		t.Errorf("pin = %q, want rename", got)
	}
	// No user strategy: rename (never ask).
	if got := watchConflictStrategy(&database.User{}, ""); got != tcbdata.ConflictStrategyRename {
		t.Errorf("default = %q, want rename", got)
	}
	// User's non-interactive strategy is honored.
	if got := watchConflictStrategy(&database.User{ConflictStrategy: tcbdata.ConflictStrategySkip}, ""); got != tcbdata.ConflictStrategySkip {
		t.Errorf("user skip = %q, want skip", got)
	}
	if got := watchConflictStrategy(&database.User{ConflictStrategy: tcbdata.ConflictStrategyOverwrite}, ""); got != tcbdata.ConflictStrategyOverwrite {
		t.Errorf("user overwrite = %q, want overwrite", got)
	}
	// User's ASK must be downgraded to rename: an unattended prompt would
	// silently drop the files.
	if got := watchConflictStrategy(&database.User{ConflictStrategy: tcbdata.ConflictStrategyAsk}, ""); got != tcbdata.ConflictStrategyRename {
		t.Errorf("user ask = %q, want rename", got)
	}
}

// historyRowName shortens stored paths to their base name.
func TestHistoryRowName(t *testing.T) {
	cases := map[string]string{
		"videos/a.mp4": "a.mp4",
		"a/b/c.bin":    "c.bin",
		"plain.bin":    "plain.bin",
		"":             "?",
	}
	for in, want := range cases {
		if got := historyRowName(in); got != want {
			t.Errorf("historyRowName(%q) = %q, want %q", in, got, want)
		}
	}
}

// SourceFromTGFile derives the ledger origin from a task file.
func TestSourceFromTGFile(t *testing.T) {
	chatID, msgID := tgutil.SourceFromTGFile(buildGroupFile(t, "", "x.jpg", 1))
	if msgID == 0 {
		t.Error("message ID must be derived from the file's message")
	}
	_ = chatID
}
