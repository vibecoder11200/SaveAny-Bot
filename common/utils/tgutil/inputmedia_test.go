package tgutil

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

type resendFile struct {
	location tg.InputFileLocationClass
	message  *tg.Message
}

func (f *resendFile) Location() tg.InputFileLocationClass { return f.location }
func (f *resendFile) Dler() downloader.Client             { return nil }
func (f *resendFile) Size() int64                         { return 0 }
func (f *resendFile) Name() string                        { return "x.bin" }
func (f *resendFile) SetName(string)                      {}
func (f *resendFile) FileUniqueID() string                { return "doc:1" }
func (f *resendFile) Message() *tg.Message                { return f.message }
func (f *resendFile) RefreshLocation(tg.MessageMediaClass) error {
	return errors.New("not implemented")
}

var _ tfile.TGFileMessage = (*resendFile)(nil)

func docLocation(ref string) *tg.InputDocumentFileLocation {
	return &tg.InputDocumentFileLocation{
		ID:            11,
		AccessHash:    22,
		FileReference: []byte(ref),
		ThumbSize:     "x",
	}
}

func photoLocation(ref string) *tg.InputPhotoFileLocation {
	return &tg.InputPhotoFileLocation{
		ID:            33,
		AccessHash:    44,
		FileReference: []byte(ref),
		ThumbSize:     "x",
	}
}

func TestResendInputMediaFromDocumentLocation(t *testing.T) {
	media, ok := ResendInputMedia(&resendFile{
		location: docLocation("fresh"),
		message:  &tg.Message{ID: 1},
	})
	if !ok {
		t.Fatal("document location must build an input media")
	}
	doc, ok := media.(*tg.InputMediaDocument)
	if !ok {
		t.Fatalf("media type = %T", media)
	}
	input, ok := doc.ID.(*tg.InputDocument)
	if !ok {
		t.Fatalf("id type = %T", doc.ID)
	}
	if input.ID != 11 || input.AccessHash != 22 || string(input.FileReference) != "fresh" {
		t.Fatalf("input document %+v", input)
	}
}

func TestResendInputMediaFromPhotoLocation(t *testing.T) {
	media, ok := ResendInputMedia(&resendFile{
		location: photoLocation("ref"),
		message:  &tg.Message{ID: 2},
	})
	if !ok {
		t.Fatal("photo location must build an input media")
	}
	photo, ok := media.(*tg.InputMediaPhoto)
	if !ok {
		t.Fatalf("media type = %T", media)
	}
	input, ok := photo.ID.(*tg.InputPhoto)
	if !ok {
		t.Fatalf("id type = %T", photo.ID)
	}
	if input.ID != 33 || input.AccessHash != 44 || string(input.FileReference) != "ref" {
		t.Fatalf("input photo %+v", input)
	}
}

func TestResendInputMediaGates(t *testing.T) {
	// noforwards source must never be resent.
	protected := &tg.Message{ID: 3}
	protected.SetNoforwards(true)
	if _, ok := ResendInputMedia(&resendFile{
		location: docLocation("ref"),
		message:  protected,
	}); ok {
		t.Fatal("noforwards message must not build input media")
	}
	// nil message (no source to gate) must not resend.
	if _, ok := ResendInputMedia(&resendFile{location: docLocation("ref")}); ok {
		t.Fatal("message-less file must not build input media")
	}
	// unsupported location must not resend.
	if _, ok := ResendInputMedia(&resendFile{
		location: &tg.InputSecureFileLocation{},
		message:  &tg.Message{ID: 4},
	}); ok {
		t.Fatal("unsupported location must not build input media")
	}
}

func TestIsResendFallbackError(t *testing.T) {
	fallbacks := []string{
		"FILE_REFERENCE_EXPIRED",
		"MEDIA_EMPTY",
		"CHAT_FORWARDS_RESTRICTED",
		"PEER_ID_INVALID",
		"CHANNEL_PRIVATE",
	}
	for _, code := range fallbacks {
		err := tgerr.New(400, code)
		if !IsResendFallbackError(err) {
			t.Fatalf("%s must be fallback-eligible", code)
		}
		if !ResendCanFallback(err) {
			t.Fatalf("%s must allow fallback", code)
		}
	}
	// Pre-flight wrapper always allows fallback.
	if !ResendCanFallback(storagetypes.ErrResendPreflight) {
		t.Fatal("preflight error must allow fallback")
	}
	// Unknown/transport errors are ambiguous: no fallback.
	ambiguous := []error{
		errors.New("connection reset"),
		tgerr.New(500, "INTERNAL"),
		tgerr.New(420, "FLOOD_WAIT_30"),
		context.DeadlineExceeded,
	}
	for _, err := range ambiguous {
		if ResendCanFallback(err) {
			t.Fatalf("ambiguous error %v must not allow fallback", err)
		}
	}
	if ResendCanFallback(nil) {
		t.Fatal("nil must not be a fallback trigger")
	}
}
