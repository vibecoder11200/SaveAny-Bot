// Package mediafilter applies the instance-wide allowed media type setting.
package mediafilter

import (
	"github.com/gotd/td/tg"
	"github.com/krau/SaveAny-Bot/config"
	"github.com/krau/SaveAny-Bot/pkg/mediatype"
)

// Allowed reports whether media should be processed under the current
// allowlist. An empty allowlist permits every supported media object.
func Allowed(media tg.MessageMediaClass) bool {
	allowed := config.C().Media.AllowedMediaTypes
	if len(allowed) == 0 {
		return true
	}
	typ, ok := mediatype.Classify(media)
	if !ok {
		return false
	}
	return Allows(typ)
}

// Allows reports whether a known media category is enabled.
func Allows(typ mediatype.Type) bool {
	allowed := config.C().Media.AllowedMediaTypes
	if len(allowed) == 0 {
		return true
	}
	for _, value := range allowed {
		if value == string(typ) {
			return true
		}
	}
	return false
}

// TokenSet is one resolved restriction layer: the set of accepted
// fine-grained media type tokens (the mediatype.Fine* constants). Declared
// as a type alias so the MediaFilter defined type passes without conversion.
type TokenSet = map[string]bool

// AllowedFor applies the scope layers in precedence order: scopes are
// given most-specific first (command t: > per-watch mediatype > per-user
// default) and the FIRST non-nil one applies — a more specific scope
// REPLACES a broader one, mirroring the fork (an explicit t: overrides the
// stored default rather than intersecting with it). The instance-wide
// allowlist is evaluated FIRST and can never be bypassed by any scope.
// Scope membership is classified with the fine-grained tokens
// (mediatype.FineType), because the scope grammar separates voice,
// animation and sticker inside the instance gate's coarse categories.
// With no non-nil scope this is exactly Allowed.
func AllowedFor(media tg.MessageMediaClass, scopes ...TokenSet) bool {
	if !Allowed(media) {
		return false
	}
	var scope TokenSet
	for _, s := range scopes {
		if s != nil {
			scope = s
			break
		}
	}
	if scope == nil {
		return true
	}
	return scope[FineType(media)]
}

// FineType classifies media into the fine-grained filter token used by the
// scope layers (delegates to mediatype.FineType; see there for the grammar).
func FineType(media tg.MessageMediaClass) string {
	return mediatype.FineType(media)
}
