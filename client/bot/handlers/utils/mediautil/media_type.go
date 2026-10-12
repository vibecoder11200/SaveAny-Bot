package mediautil

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/database"
)

// Canonical media type tokens. MediaTypePhoto is only ever produced for
// *tg.MessageMediaPhoto; a document carrying an image MIME type (e.g.
// image/jpeg without a photo media wrapper) is classified as
// MediaTypeDocument.
const (
	MediaTypePhoto     = "photo"
	MediaTypeVideo     = "video"
	MediaTypeAnimation = "animation"
	MediaTypeAudio     = "audio"
	MediaTypeVoice     = "voice"
	MediaTypeSticker   = "sticker"
	MediaTypeDocument  = "document"
)

// MediaTypeTokens lists the canonical tokens accepted by ParseMediaFilter,
// in display order.
var MediaTypeTokens = []string{
	MediaTypePhoto,
	MediaTypeVideo,
	MediaTypeAnimation,
	MediaTypeAudio,
	MediaTypeVoice,
	MediaTypeSticker,
	MediaTypeDocument,
}

// mediaTypeAliases map user-friendly aliases to canonical tokens.
var mediaTypeAliases = map[string]string{
	"gif":   MediaTypeAnimation,
	"music": MediaTypeAudio,
	"vid":   MediaTypeVideo,
	"img":   MediaTypePhoto,
	"doc":   MediaTypeDocument,
}

// MediaType classifies a message media into one of the MediaType tokens.
// It is a pure function: no RPC, no I/O. Unsupported or nil media yields ""
// (callers are expected to gate on IsSupported first).
func MediaType(media tg.MessageMediaClass) string {
	switch media := media.(type) {
	case *tg.MessageMediaPhoto:
		return MediaTypePhoto
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.AsNotEmpty()
		if !ok {
			return ""
		}
		// Tiered scan so classification is independent of attribute order:
		// sticker > animation > voice/audio > video > document.
		for _, attr := range doc.Attributes {
			if _, ok := attr.(*tg.DocumentAttributeSticker); ok {
				return MediaTypeSticker
			}
		}
		for _, attr := range doc.Attributes {
			if _, ok := attr.(*tg.DocumentAttributeAnimated); ok {
				return MediaTypeAnimation
			}
		}
		for _, attr := range doc.Attributes {
			switch attr := attr.(type) {
			case *tg.DocumentAttributeAudio:
				// Voice messages are audio documents carrying the voice
				// flag (MTProto has no dedicated voice attribute type).
				if attr.Voice {
					return MediaTypeVoice
				}
				return MediaTypeAudio
			case *tg.DocumentAttributeVideo:
				// Round video messages also carry this attribute.
				return MediaTypeVideo
			}
		}
		return MediaTypeDocument
	default:
		return ""
	}
}

// MediaFilter is a resolved set of allowed media type tokens. A nil filter
// allows everything (no filtering configured).
type MediaFilter map[string]bool

// ParseMediaFilter parses a comma-separated media type spec such as
// "video,photo" (spaces are tolerated). An empty spec yields nil, meaning
// "allow everything". Aliases: gif→animation, music→audio, vid→video,
// img→photo, doc→document. Unknown tokens return an error naming the token
// and the valid tokens — callers surface this to the user instead of
// silently passing everything through (fail-closed).
func ParseMediaFilter(spec string) (MediaFilter, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	filter := make(MediaFilter)
	for _, raw := range strings.Split(spec, ",") {
		token := strings.ToLower(strings.TrimSpace(raw))
		if token == "" {
			continue
		}
		if canonical, ok := mediaTypeAliases[token]; ok {
			token = canonical
		}
		if !validToken(token) {
			return nil, fmt.Errorf("unknown media type %q (valid: %s)",
				token, strings.Join(MediaTypeTokens, ", "))
		}
		filter[token] = true
	}
	if len(filter) == 0 {
		return nil, nil
	}
	return filter, nil
}

func validToken(token string) bool {
	for _, t := range MediaTypeTokens {
		if t == token {
			return true
		}
	}
	return false
}

// Allows reports whether the given media type passes the filter. A nil
// filter allows everything; with a filter set, an unclassifiable media type
// ("") is rejected.
func (f MediaFilter) Allows(mediaType string) bool {
	if f == nil {
		return true
	}
	return f[mediaType]
}

// IsMediaFilterSpec reports whether an argument is a media filter spec:
// anything with the literal "t:" prefix. Legacy regexes starting with
// "^t:" or containing "[^t]" do not start with "t:" and remain regexes.
func IsMediaFilterSpec(arg string) bool {
	return strings.HasPrefix(arg, "t:")
}

// IsValidMediaTypeToken reports whether the token is one of the canonical
// media type tokens accepted in stored filter values.
func IsValidMediaTypeToken(token string) bool {
	return validToken(token)
}

// ToggleMediaFilterToken flips the token's presence in a stored
// comma-separated filter spec and returns the canonical form.
func ToggleMediaFilterToken(spec, token string) string {
	filter, err := ParseMediaFilter(spec)
	if err != nil || filter == nil {
		filter = MediaFilter{}
	}
	if filter[token] {
		delete(filter, token)
	} else {
		filter[token] = true
	}
	tokens := make([]string, 0, len(filter))
	for _, t := range MediaTypeTokens {
		if filter[t] {
			tokens = append(tokens, t)
		}
	}
	return strings.Join(tokens, ",")
}

// UserMediaFilter resolves the per-user default media filter from the
// stored column. Stored values are only ever written through validated
// paths (the /config toggle and the t: argument parse), so a parse failure
// here means corrupt data: log a warning and return nil (allow everything)
// rather than crash the handler.
func UserMediaFilter(user *database.User) MediaFilter {
	if user == nil || user.MediaFilter == "" {
		return nil
	}
	filter, err := ParseMediaFilter(user.MediaFilter)
	if err != nil {
		log.Warnf("invalid stored media filter for user %d: %v", user.ChatID, err)
		return nil
	}
	return filter
}
