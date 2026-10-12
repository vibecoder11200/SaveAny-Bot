package mediautil

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func docWithAttributes(attrs ...tg.DocumentAttributeClass) *tg.MessageMediaDocument {
	return &tg.MessageMediaDocument{
		Document: &tg.Document{
			ID:         1,
			AccessHash: 2,
			MimeType:   "application/octet-stream",
			Attributes: attrs,
		},
	}
}

func TestMediaType(t *testing.T) {
	tests := []struct {
		name  string
		media tg.MessageMediaClass
		want  string
	}{
		{
			name:  "photo media",
			media: &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 1, AccessHash: 2, Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 1, H: 1}}}},
			want:  MediaTypePhoto,
		},
		{
			name:  "plain document",
			media: docWithAttributes(&tg.DocumentAttributeFilename{FileName: "a.zip"}),
			want:  MediaTypeDocument,
		},
		{
			name:  "image mime document is still document",
			media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 1, AccessHash: 2, MimeType: "image/jpeg", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "a.jpg"}}}},
			want:  MediaTypeDocument,
		},
		{
			name:  "video",
			media: docWithAttributes(&tg.DocumentAttributeVideo{Duration: 10, W: 100, H: 100}),
			want:  MediaTypeVideo,
		},
		{
			name:  "round video is video",
			media: docWithAttributes(&tg.DocumentAttributeVideo{Duration: 10, RoundMessage: true}),
			want:  MediaTypeVideo,
		},
		{
			name:  "audio",
			media: docWithAttributes(&tg.DocumentAttributeAudio{Duration: 10, Title: "song"}),
			want:  MediaTypeAudio,
		},
		{
			name:  "voice message",
			media: docWithAttributes(&tg.DocumentAttributeAudio{Duration: 10, Voice: true}),
			want:  MediaTypeVoice,
		},
		{
			name:  "sticker",
			media: docWithAttributes(&tg.DocumentAttributeSticker{Alt: "☺", Stickerset: &tg.InputStickerSetEmpty{}}, &tg.DocumentAttributeFilename{FileName: "sticker.webp"}),
			want:  MediaTypeSticker,
		},
		{
			name:  "animation",
			media: docWithAttributes(&tg.DocumentAttributeAnimated{}, &tg.DocumentAttributeFilename{FileName: "gif.mp4"}),
			want:  MediaTypeAnimation,
		},
		{
			name:  "animated sticker wins over animation",
			media: docWithAttributes(&tg.DocumentAttributeAnimated{}, &tg.DocumentAttributeSticker{Alt: "☺", Stickerset: &tg.InputStickerSetEmpty{}}),
			want:  MediaTypeSticker,
		},
		{
			name:  "empty document is not classified",
			media: &tg.MessageMediaDocument{Document: &tg.DocumentEmpty{}},
			want:  "",
		},
		{
			name:  "unsupported media",
			media: &tg.MessageMediaGeo{Geo: &tg.GeoPointEmpty{}},
			want:  "",
		},
		{
			name:  "nil media",
			media: nil,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MediaType(tt.media); got != tt.want {
				t.Errorf("MediaType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseMediaFilter(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    MediaFilter
		wantErr bool
	}{
		{name: "empty spec yields nil filter", spec: "", want: nil},
		{name: "whitespace spec yields nil filter", spec: "   ", want: nil},
		{
			name: "plain tokens",
			spec: "video,photo",
			want: MediaFilter{MediaTypeVideo: true, MediaTypePhoto: true},
		},
		{
			name: "spaces and case tolerated",
			spec: " Video , photo ",
			want: MediaFilter{MediaTypeVideo: true, MediaTypePhoto: true},
		},
		{
			name: "aliases resolve to canonical",
			spec: "gif,music,vid,img,doc",
			want: MediaFilter{
				MediaTypeAnimation: true,
				MediaTypeAudio:     true,
				MediaTypeVideo:     true,
				MediaTypePhoto:     true,
				MediaTypeDocument:  true,
			},
		},
		{
			name:    "unknown token fails with token in message",
			spec:    "video,viseo",
			wantErr: true,
		},
		{
			name:    "comma only yields nil filter",
			spec:    ",,,",
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMediaFilter(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseMediaFilter(%q) expected error, got filter %v", tt.spec, got)
				}
				if tt.spec == "video,viseo" && !strings.Contains(err.Error(), "viseo") {
					t.Errorf("error should name the offending token, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMediaFilter(%q) unexpected error: %v", tt.spec, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParseMediaFilter(%q) = %v, want %v", tt.spec, got, tt.want)
			}
			for token := range tt.want {
				if !got[token] {
					t.Errorf("ParseMediaFilter(%q) missing token %q", tt.spec, token)
				}
			}
		})
	}
}

func TestMediaFilterAllows(t *testing.T) {
	var nilFilter MediaFilter
	if !nilFilterAllows(nilFilter, MediaTypeVideo) {
		t.Error("nil filter must allow everything")
	}
	filter := MediaFilter{MediaTypeVideo: true}
	if !filter.Allows(MediaTypeVideo) {
		t.Error("filter containing video must allow video")
	}
	if filter.Allows(MediaTypePhoto) {
		t.Error("filter without photo must reject photo")
	}
	if filter.Allows("") {
		t.Error("filter must reject unclassifiable media type")
	}
}

func nilFilterAllows(f MediaFilter, mt string) bool { return f.Allows(mt) }

func TestIsMediaFilterSpec(t *testing.T) {
	tests := []struct {
		arg  string
		want bool
	}{
		{"t:video,photo", true},
		{"t:vid", true},
		{"t:bogus", true},
		{"t:", true},
		{"^t:foo$", false},
		{"video,photo", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsMediaFilterSpec(tt.arg); got != tt.want {
			t.Errorf("IsMediaFilterSpec(%q) = %v, want %v", tt.arg, got, tt.want)
		}
	}
}
