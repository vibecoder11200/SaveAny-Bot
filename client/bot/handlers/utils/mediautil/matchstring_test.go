package mediautil

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/common/utils/tgutil"
)

// matchStringLegacy is the pre-refactor /save builder, kept here verbatim as
// the parity fixture: MatchStringForFilter must be byte-identical to it.
func matchStringLegacy(msg *tg.Message, media tg.MessageMediaClass) string {
	var sb strings.Builder
	sb.WriteString(msg.GetMessage())
	sb.WriteString(" ")
	fn, _ := tgutil.GetMediaFileName(media)
	sb.WriteString(fn)
	return sb.String()
}

// TestMatchStringForFilterParity must build from a message whose media flag
// is set, exactly like wire messages: GetMedia is a conditional getter.
func TestMatchStringForFilterParity(t *testing.T) {
	doc := &tg.Document{
		ID:         4242,
		Size:       10,
		MimeType:   "video/mp4",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "clip.mp4"}},
	}
	media := &tg.MessageMediaDocument{Document: doc}
	msg := &tg.Message{Message: "4k: sunset"}
	msg.Flags.Set(9)
	msg.Media = media
	got := MatchStringForFilter(msg)
	want := matchStringLegacy(msg, media)
	if got != want {
		t.Fatalf("MatchStringForFilter = %q, legacy = %q", got, want)
	}
	if got != "4k: sunset clip.mp4" {
		t.Errorf("unexpected match string %q", got)
	}

	// Document without a filename attribute: the raw GetMediaFileName
	// fallback (<docID>.<ext>) is used, NOT the sanitized tfile name.
	bare := &tg.Document{
		ID:       777,
		Size:     10,
		MimeType: "video/mp4",
	}
	bareMedia := &tg.MessageMediaDocument{Document: bare}
	bareMsg := &tg.Message{Message: ""}
	bareMsg.Flags.Set(9)
	bareMsg.Media = bareMedia
	got = MatchStringForFilter(bareMsg)
	want = matchStringLegacy(bareMsg, bareMedia)
	if got != want {
		t.Fatalf("bare doc: MatchStringForFilter = %q, legacy = %q", got, want)
	}
	// The GetMediaFileName mime fallback itself yields "<id>..mp4" (the
	// extension carries the dot) — the point is byte parity with /save.
	if got != " 777..mp4" {
		t.Errorf("bare doc match string = %q, want \" 777..mp4\"", got)
	}
}
