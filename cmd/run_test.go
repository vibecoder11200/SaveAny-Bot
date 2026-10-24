package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/krau/SaveAny-Bot/common/tdler"
)

func TestCleanCacheDirSpareResumeAndRemoveLegacySibling(t *testing.T) {
	base := t.TempDir()
	staleFile := filepath.Join(base, "old_xid_video.mp4")
	resumeDir := filepath.Join(base, tdler.ResumeDirName)
	resumeState := filepath.Join(resumeDir, "abc.part")
	legacySibling := base + "-resume"
	legacyState := filepath.Join(legacySibling, "def.part")
	for _, p := range []string{staleFile, resumeState, legacyState} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	if err := cleanCacheDir(base); err != nil {
		t.Fatalf("cleanCacheDir: %v", err)
	}

	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Error("stale cache file must be removed")
	}
	// The resume subdirectory survives the boot clean — that is the entire
	// point of resumable downloads.
	if _, err := os.Stat(resumeState); err != nil {
		t.Fatalf("resume state must survive: %v", err)
	}
	if _, err := os.Stat(legacySibling); !os.IsNotExist(err) {
		t.Error("legacy sibling dir must be removed")
	}
}
