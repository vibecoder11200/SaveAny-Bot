package mediafilter

import (
	"strings"
	"testing"
)

func TestParseMediaFilter(t *testing.T) {
	filter, err := ParseMediaFilter("video, photo")
	if err != nil {
		t.Fatal(err)
	}
	if !filter.Allows("video") || !filter.Allows("photo") {
		t.Fatal("video and photo must be allowed")
	}
	if filter.Allows("document") {
		t.Fatal("document must be rejected")
	}

	// Aliases.
	filter, err = ParseMediaFilter("gif,music,vid,img,doc")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"animation", "audio", "video", "photo", "document"} {
		if !filter.Allows(want) {
			t.Fatalf("alias target %q must be allowed", want)
		}
	}
	if filter.Allows("voice") {
		t.Fatal("voice must be rejected")
	}

	// Empty spec = nil (allow everything).
	if f, err := ParseMediaFilter("  "); err != nil || f != nil {
		t.Fatalf("empty spec must give nil filter, got %v err %v", f, err)
	}
	// "t:,," style comma-only spec is also empty.
	if f, err := ParseMediaFilter(", ,"); err != nil || f != nil {
		t.Fatalf("comma-only spec must give nil filter, got %v err %v", f, err)
	}

	// Unknown token fails closed.
	if _, err := ParseMediaFilter("video,mpeg"); err == nil || !strings.Contains(err.Error(), "mpeg") {
		t.Fatalf("unknown token must error naming it, got %v", err)
	}
}

func TestIsMediaFilterSpec(t *testing.T) {
	if !IsMediaFilterSpec("t:video") || IsMediaFilterSpec("video") {
		t.Fatal("prefix detection broken")
	}
	// Legacy regexes starting with ^t: or containing [^t] are not specs.
	if IsMediaFilterSpec("^t:foo") {
		t.Fatal("^-anchored regex must stay a regex")
	}
}

func TestAllowsNilFilter(t *testing.T) {
	var f MediaFilter
	if !f.Allows("") || !f.Allows("video") {
		t.Fatal("nil filter allows everything")
	}
	set := MediaFilter{"video": true}
	if set.Allows("") {
		t.Fatal("a set filter must reject unclassifiable media")
	}
}

func TestToggleMediaFilterToken(t *testing.T) {
	if got := ToggleMediaFilterToken("", "video"); got != "video" {
		t.Fatalf("toggle on empty = %q, want video", got)
	}
	if got := ToggleMediaFilterToken("video,photo", "video"); got != "photo" {
		t.Fatalf("toggle off = %q, want photo", got)
	}
	// Canonical order and alias input.
	if got := ToggleMediaFilterToken("photo", "doc"); got != "photo,document" {
		t.Fatalf("canonical order = %q", got)
	}
}
