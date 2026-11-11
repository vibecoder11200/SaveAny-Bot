package mediafilter

import (
	"fmt"
	"strings"

	"github.com/krau/SaveAny-Bot/pkg/mediatype"
)

// MediaFilter is a resolved set of allowed fine media type tokens (the
// mediatype.Fine* constants). A nil filter allows everything (no filtering
// configured).
type MediaFilter map[string]bool

// ParseMediaFilter parses a comma-separated fine media type spec such as
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
		if !mediatype.IsFineToken(token) {
			return nil, fmt.Errorf("unknown media type %q (valid: %s)",
				token, strings.Join(mediatype.FineTokens, ", "))
		}
		filter[token] = true
	}
	if len(filter) == 0 {
		return nil, nil
	}
	return filter, nil
}

// mediaTypeAliases map user-friendly aliases to canonical tokens.
var mediaTypeAliases = map[string]string{
	"gif":   mediatype.FineAnimation,
	"music": mediatype.FineAudio,
	"vid":   mediatype.FineVideo,
	"img":   mediatype.FinePhoto,
	"doc":   mediatype.FineDocument,
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
	return mediatype.IsFineToken(token)
}

// ToggleMediaFilterToken flips the token's presence in a stored
// comma-separated filter spec and returns the canonical form.
func ToggleMediaFilterToken(spec, token string) string {
	if canonical, ok := mediaTypeAliases[token]; ok {
		token = canonical
	}
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
	for _, t := range mediatype.FineTokens {
		if filter[t] {
			tokens = append(tokens, t)
		}
	}
	return strings.Join(tokens, ",")
}
