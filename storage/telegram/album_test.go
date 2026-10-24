package telegram

import (
	"fmt"
	"testing"

	"github.com/gotd/td/tgerr"
)

func TestUsableVideoMeta(t *testing.T) {
	cases := []struct {
		name string
		meta *VideoMetadata
		want bool
	}{
		{"nil", nil, false},
		{"zeros", &VideoMetadata{}, false},
		{"no duration", &VideoMetadata{Width: 1920, Height: 1080}, false},
		{"no width", &VideoMetadata{Duration: 10, Height: 1080}, false},
		{"no height", &VideoMetadata{Duration: 10, Width: 1920}, false},
		{"complete", &VideoMetadata{Duration: 10, Width: 1920, Height: 1080}, true},
	}
	for _, c := range cases {
		if got := usableVideoMeta(c.meta); got != c.want {
			t.Errorf("%s: usableVideoMeta = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAlbumMixesDocWithMedia(t *testing.T) {
	cases := []struct {
		name  string
		kinds []string
		want  bool
	}{
		{"all videos", []string{mediaKindVideo, mediaKindVideo}, false},
		{"photos and videos", []string{mediaKindPhoto, mediaKindVideo}, false},
		{"all docs", []string{mediaKindDoc, mediaKindDoc}, false},
		{"one doc among videos", []string{mediaKindVideo, mediaKindDoc, mediaKindVideo}, true},
		{"one doc among photos", []string{mediaKindPhoto, mediaKindDoc}, true},
		{"single doc", []string{mediaKindDoc}, false},
	}
	for _, c := range cases {
		prepared := make([]preparedMedia, 0, len(c.kinds))
		for _, kind := range c.kinds {
			prepared = append(prepared, preparedMedia{kind: kind})
		}
		if got := albumMixesDocWithMedia(prepared); got != c.want {
			t.Errorf("%s: albumMixesDocWithMedia = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsMediaRejectedByTelegram(t *testing.T) {
	rejected := []string{
		"MEDIA_INVALID",
		"MEDIA_EMPTY",
		"PHOTO_EXT_INVALID",
		"PHOTO_INVALID_DIMENSIONS",
		"VIDEO_INVALID",
	}
	for _, typ := range rejected {
		if !isMediaRejectedByTelegram(tgerr.New(400, typ)) {
			t.Errorf("%s should be rejected", typ)
		}
		// The predicate must see through error wrapping.
		if !isMediaRejectedByTelegram(fmt.Errorf("send album: %w", tgerr.New(400, typ))) {
			t.Errorf("%s should be rejected through wrapping", typ)
		}
	}
	allowed := []string{"FLOOD_WAIT", "TIMEOUT", "PEER_ID_INVALID", "INTERNAL"}
	for _, typ := range allowed {
		if isMediaRejectedByTelegram(tgerr.New(400, typ)) {
			t.Errorf("%s must not be treated as a media rejection", typ)
		}
	}
	if isMediaRejectedByTelegram(nil) {
		t.Error("nil must not be a media rejection")
	}
}

func TestDescribeAlbum(t *testing.T) {
	prepared := []preparedMedia{
		{kind: mediaKindVideo, filename: "a.mp4"},
		{kind: mediaKindDoc, filename: "b.mp4"},
	}
	if got := describeAlbum(prepared); got != "video(a.mp4), doc(b.mp4)" {
		t.Fatalf("describeAlbum = %q", got)
	}
}
