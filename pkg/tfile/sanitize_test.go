package tfile

import (
	"strings"
	"testing"
)

func TestSanitizeFileName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"../../etc/passwd", "passwd"},
		{"a/b.mp4", "b.mp4"},
		{`a\..\..\win\evil.exe`, "evil.exe"},
		{"normal.mp4", "normal.mp4"},
		{"..", "file"},
		{"", ""},
		{"my file (2026).mkv", "my file (2026).mkv"},
		{"bad" + `<>:"|?*` + "name.txt", "bad" + strings.Repeat("_", 7) + "name.txt"},
	}
	for _, tt := range tests {
		if got := SanitizeFileName(tt.input); got != tt.want {
			t.Errorf("SanitizeFileName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTGFileNameAlwaysSanitized(t *testing.T) {
	// Construction, rename and options must all sanitize: the journal and
	// destination paths must never see a traversing name.
	f := NewTGFile(nil, nil, 0, "../../etc/passwd")
	if got := f.Name(); got != "passwd" {
		t.Errorf("NewTGFile name = %q, want %q", got, "passwd")
	}
	f.SetName("a/b.mp4")
	if got := f.Name(); got != "b.mp4" {
		t.Errorf("SetName name = %q, want %q", got, "b.mp4")
	}
	f2 := NewTGFile(nil, nil, 0, "", WithName("../../etc/host.conf"))
	if got := f2.Name(); got != "host.conf" {
		t.Errorf("WithName name = %q, want %q", got, "host.conf")
	}
	f3 := NewTGFile(nil, nil, 0, "", WithNameIfEmpty("x/../y.mp4"))
	if got := f3.Name(); got != "y.mp4" {
		t.Errorf("WithNameIfEmpty name = %q, want %q", got, "y.mp4")
	}
}
