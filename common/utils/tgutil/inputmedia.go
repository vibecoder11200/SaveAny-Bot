package tgutil

import (
	"errors"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/krau/SaveAny-Bot/pkg/storagetypes"
	"github.com/krau/SaveAny-Bot/pkg/tfile"
)

// ResendInputMedia builds the messages.sendMedia input for re-sending a
// file's source media without re-uploading it. The input is built from the
// file's *current location* — after a reference refresh it carries a fresh
// file_reference — never from the source message, whose embedded reference
// can be hours old in a long batch. The source message is only consulted for
// the noforwards gate: forward-protected media must not be re-sent.
func ResendInputMedia(file tfile.TGFileMessage) (tg.InputMediaClass, bool) {
	if msg := file.Message(); msg == nil || msg.GetNoforwards() {
		return nil, false
	}
	switch loc := file.Location().(type) {
	case *tg.InputDocumentFileLocation:
		return &tg.InputMediaDocument{
			ID: &tg.InputDocument{
				ID:            loc.ID,
				AccessHash:    loc.AccessHash,
				FileReference: loc.FileReference,
			},
		}, true
	case *tg.InputPhotoFileLocation:
		return &tg.InputMediaPhoto{
			ID: &tg.InputPhoto{
				ID:            loc.ID,
				AccessHash:    loc.AccessHash,
				FileReference: loc.FileReference,
			},
		}, true
	}
	return nil, false
}

// ResendCanFallback reports whether a failed re-send is known to have
// delivered nothing, making the download+upload fallback safe: pre-flight
// rejections (no peer, protected media) plus the Telegram error classes in
// IsResendFallbackError. Anything else must fail the element — re-uploading
// after an ambiguous send would create a guaranteed duplicate.
func ResendCanFallback(err error) bool {
	return errors.Is(err, storagetypes.ErrResendPreflight) || IsResendFallbackError(err)
}

// IsResendFallbackError reports whether a failed re-send is *known* not to
// have delivered anything, so falling back to the normal download+upload path
// is safe. Only Telegram-rejected input and unreachable destinations qualify:
// an ambiguous failure (timeout after the server accepted the message, etc.)
// must fail the element instead — re-uploading on top of a delivered resend
// would create a guaranteed duplicate.
func IsResendFallbackError(err error) bool {
	if err == nil {
		return false
	}
	return tgerr.Is(err,
		// stale/invalid source media reference
		"FILE_REFERENCE_EXPIRED",
		"FILE_REFERENCE_INVALID",
		"MEDIA_EMPTY",
		"DOCUMENT_INVALID",
		"PHOTO_INVALID",
		"PHOTO_INVALID_DIMENSIONS",
		// destination unreachable or unwritable
		"PEER_ID_INVALID",
		"CHAT_ID_INVALID",
		"USER_ID_INVALID",
		"CHANNEL_INVALID",
		"CHANNEL_PRIVATE",
		"CHANNEL_PEER_INVALID",
		"CHAT_WRITE_FORBIDDEN",
		"CHANNEL_PUBLIC_GROUP_NA",
		// forward protection at chat level
		"CHAT_FORWARDS_RESTRICTED",
	)
}
