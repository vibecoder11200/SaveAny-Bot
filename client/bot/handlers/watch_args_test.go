package handlers

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestParseWatchArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		chat      string
		msgRe     string
		mediaType string
		wantErr   error
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
			args:    []string{"chat", "storage:foo"},
			chat:    "chat",
			wantErr: errWatchFilterTypeUnsupported,
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
