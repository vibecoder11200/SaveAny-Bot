package dirutil

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeRelDirPath(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "empty means root", in: "", want: ""},
		{name: "plain segment", in: "photos", want: "photos"},
		{name: "interior separators kept", in: "a/b/c", want: "a/b/c"},
		{name: "dot segments collapsed", in: "a/./b", want: "a/b"},
		{name: "double dot dropped", in: "a/../b", want: "a/b"},
		{name: "leading double dot dropped", in: "../etc/passwd", want: "etc/passwd"},
		{name: "only double dots becomes root", in: "..", want: ""},
		{name: "leading slash rejected", in: "/etc", wantErr: ErrRelDirPathAbsolute},
		{name: "windows separators normalized", in: `a\b\c`, want: "a/b/c"},
		{name: "windows absolute rejected", in: `C:\Users`, wantErr: ErrRelDirPathAbsolute},
		{name: "windows absolute forward slash rejected", in: "C:/Users", wantErr: ErrRelDirPathAbsolute},
		{name: "unc path rejected", in: `\\server\share`, wantErr: ErrRelDirPathAbsolute},
		{name: "home shortcut rejected", in: "~/secret", wantErr: ErrRelDirPathAbsolute},
		{name: "unicode kept", in: "照片/2024", want: "照片/2024"},
		{name: "trailing slash trimmed", in: "a/b/", want: "a/b"},
		{name: "duplicate separators collapsed", in: "a//b", want: "a/b"},
		{
			name:    "length cap",
			in:      strings.Repeat("a/", 300),
			wantErr: errors.New("dir path too long (max 512 characters)"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SanitizeRelDirPath(tt.in)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("SanitizeRelDirPath(%q) = %q, want error %v", tt.in, got, tt.wantErr)
				}
				if tt.wantErr == ErrRelDirPathAbsolute && !errors.Is(err, ErrRelDirPathAbsolute) {
					t.Fatalf("error = %v, want %v", err, ErrRelDirPathAbsolute)
				}
				if tt.wantErr.Error() == "dir path too long (max 512 characters)" && err.Error() != tt.wantErr.Error() {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SanitizeRelDirPath(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("SanitizeRelDirPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// No output may ever traverse: whatever comes in, the result must not contain
// a ".." segment nor start with a separator.
func TestSanitizeRelDirPathNeverTraverses(t *testing.T) {
	traversal := []string{
		"../..", "../../etc/passwd", "a/../../b", `..\..\windows`,
		"a/b/../../../../../../root/.ssh", "..%2f..%2fetc", "!$evil/../../x",
	}
	for _, in := range traversal {
		got, err := SanitizeRelDirPath(in)
		if err != nil {
			if !errors.Is(err, ErrRelDirPathAbsolute) {
				t.Errorf("SanitizeRelDirPath(%q) unexpected error: %v", in, err)
			}
			continue
		}
		if got == ".." || strings.HasPrefix(got, ".."+"/") || strings.HasPrefix(got, "/") || strings.HasPrefix(got, `\`) {
			t.Errorf("SanitizeRelDirPath(%q) = %q, output traverses", in, got)
		}
	}
}
