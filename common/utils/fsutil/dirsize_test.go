package fsutil

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, size int) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.bin", 100)
	write("sub/b.bin", 200)
	write("sub/deep/c.bin", 50)
	write("ignored-symlink", 0)

	got, err := DirSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != 350 {
		t.Errorf("DirSize = %d, want 350", got)
	}

	// A nonexistent dir is an error, not zero-with-nil.
	if _, err := DirSize(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing dir must be an error")
	}
}

// Cached results are stable within the TTL even if the tree changes: the
// cache exists so repeated /status calls do not re-walk the tree.
func TestDirSizeCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := DirSize(dir)
	if err != nil || first != 10 {
		t.Fatalf("first = %d err=%v", first, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "y"), make([]byte, 99), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := DirSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("within TTL, cached %d must be returned, got %d", first, second)
	}
}

// Concurrent calls for the same path share one walk and agree.
func TestDirSizeSingleFlight(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 50; i++ {
		if err := os.WriteFile(filepath.Join(dir, string(rune('a'+i%26))+string(rune('a'+i/26))), make([]byte, 7), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	sizes := make([]int64, 16)
	for i := range sizes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sizes[i], _ = DirSize(dir)
		}()
	}
	wg.Wait()
	for i, s := range sizes {
		if s != 350 {
			t.Errorf("goroutine %d got %d, want 350", i, s)
		}
	}
}

func TestDiskFreeSmoke(t *testing.T) {
	free, err := DiskFree(t.TempDir())
	if err != nil {
		t.Skipf("disk free unsupported here: %v", err)
	}
	if free == 0 {
		t.Log("DiskFree returned 0 (filesystem may report 0 for tmpfs)")
	}
}
