package mediatype

import (
	"github.com/gotd/td/tg"
)

// Fine-grained media type tokens for per-scope filters (per-user default,
// /save t: overrides, per-watch mediatype:). They are finer than the
// instance-wide coarse categories above: voice, animation and sticker are
// special audio/video/image documents the coarse gate lumps together,
// while the scope filters let users separate them.
const (
	FinePhoto     = "photo"
	FineVideo     = "video"
	FineAnimation = "animation"
	FineAudio     = "audio"
	FineVoice     = "voice"
	FineSticker   = "sticker"
	FineDocument  = "document"
)

// FineTokens lists the canonical fine tokens accepted by filter specs, in
// display order.
var FineTokens = []string{
	FinePhoto,
	FineVideo,
	FineAnimation,
	FineAudio,
	FineVoice,
	FineSticker,
	FineDocument,
}

// IsFineToken reports whether token is one of the canonical fine tokens
// accepted in stored filter values.
func IsFineToken(token string) bool {
	for _, t := range FineTokens {
		if t == token {
			return true
		}
	}
	return false
}

// FineType classifies a message media into one of the fine tokens. It is a
// pure function: no RPC, no I/O. Unsupported or nil media yields ""
// (callers gate on Classify / IsSupported first).
func FineType(media tg.MessageMediaClass) string {
	switch media := media.(type) {
	case *tg.MessageMediaPhoto:
		return FinePhoto
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.AsNotEmpty()
		if !ok {
			return ""
		}
		// Tiered scan so classification is independent of attribute order:
		// sticker > animation > voice/audio > video > document.
		for _, attr := range doc.Attributes {
			if _, ok := attr.(*tg.DocumentAttributeSticker); ok {
				return FineSticker
			}
		}
		for _, attr := range doc.Attributes {
			if _, ok := attr.(*tg.DocumentAttributeAnimated); ok {
				return FineAnimation
			}
		}
		for _, attr := range doc.Attributes {
			switch attr := attr.(type) {
			case *tg.DocumentAttributeAudio:
				// Voice messages are audio documents carrying the voice
				// flag (MTProto has no dedicated voice attribute type).
				if attr.Voice {
					return FineVoice
				}
				return FineAudio
			case *tg.DocumentAttributeVideo:
				// Round video messages also carry this attribute.
				return FineVideo
			}
		}
		return FineDocument
	default:
		return ""
	}
}
