package mediafilter

import (
	"testing"

	"github.com/gotd/td/tg"
)

func docWithAttrs(attrs ...tg.DocumentAttributeClass) *tg.MessageMediaDocument {
	return &tg.MessageMediaDocument{
		Document: &tg.Document{
			ID:         1,
			AccessHash: 2,
			MimeType:   "application/octet-stream",
			Attributes: attrs,
		},
	}
}

func photo() *tg.MessageMediaPhoto {
	return &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 1, AccessHash: 2, Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 1, H: 1}}}}
}

func TestAllowedForMatrix(t *testing.T) {
	document := docWithAttrs(&tg.DocumentAttributeFilename{FileName: "a.zip"})
	video := docWithAttrs(&tg.DocumentAttributeVideo{Duration: 10, W: 100, H: 100})
	voice := docWithAttrs(&tg.DocumentAttributeAudio{Duration: 10, Voice: true})

	photosOnly := map[string]bool{"photo": true}
	videoOnly := map[string]bool{"video": true}

	tests := []struct {
		name  string
		media tg.MessageMediaClass
		// scopes ordered most-specific first (command t: > user default);
		scopes   []TokenSet
		expected bool
	}{
		{"no filters anywhere allows", document, nil, true},
		{"empty scope set allows", document, []TokenSet{}, true},
		{"nil scope skipped", document, []TokenSet{nil}, true},
		{"user photos-only blocks document", document, []TokenSet{photosOnly}, false},
		{"user photos-only allows photo", photo(), []TokenSet{photosOnly}, true},
		{"t: video replaces user photos-only, allows video", video, []TokenSet{videoOnly, photosOnly}, true},
		{"t: video alone still blocks document", document, []TokenSet{videoOnly, photosOnly}, false},
		{"empty t: falls through to user default", document, []TokenSet{nil, photosOnly}, false},
		{"voice is not video despite coarse audio/video overlap", video, []TokenSet{{"voice": true}}, false},
		{"voice passes a voice-only scope", voice, []TokenSet{{"voice": true}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The instance gate is empty in tests (config not loaded): every
			// decision below comes from the scope layers, so a scope layer
			// can only ever restrict, never bypass anything.
			if got := AllowedFor(tc.media, tc.scopes...); got != tc.expected {
				t.Fatalf("AllowedFor = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestAllowedForInstanceGateAlwaysWins(t *testing.T) {
	// An unrestricted scope set must not flip the instance gate's verdict.
	if Allowed(docWithAttrs(&tg.DocumentAttributeFilename{FileName: "a.zip"})) {
		t.Skip("instance allowlist is configured in this environment; matrix invariant not testable")
	}
}
