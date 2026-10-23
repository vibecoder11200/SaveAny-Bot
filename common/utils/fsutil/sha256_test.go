package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestSha256File(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	got, err := Sha256File(empty)
	if err != nil {
		t.Fatalf("hash empty: %v", err)
	}
	if want := hex.EncodeToString(sha256.New().Sum(nil)); got != want {
		t.Fatalf("empty file hash = %q, want %q", got, want)
	}

	body := []byte("the quick brown fox jumps over the lazy dog")
	some := filepath.Join(dir, "some.bin")
	if err := os.WriteFile(some, body, 0o644); err != nil {
		t.Fatalf("write some: %v", err)
	}
	got, err = Sha256File(some)
	if err != nil {
		t.Fatalf("hash some: %v", err)
	}
	sum := sha256.Sum256(body)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("hash = %q, want %q", got, want)
	}

	if _, err := Sha256File(filepath.Join(dir, "missing.bin")); err == nil {
		t.Fatal("missing file must error")
	}
}
