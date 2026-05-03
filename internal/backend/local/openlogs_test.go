package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenLogFiles_Success(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	out, errLog, err := openLogFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil || errLog == nil {
		t.Fatalf("expected both files, got out=%v err=%v", out, errLog)
	}
	defer out.Close()
	defer errLog.Close()

	for _, want := range []string{"stdout.log", "stderr.log"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("%s missing: %v", want, err)
		}
	}
}

func TestOpenLogFiles_DirectoryDoesNotExist(t *testing.T) {
	t.Parallel()
	// An entirely fictitious path; openLogFiles must not silently succeed.
	out, errLog, err := openLogFiles("/nonexistent/path/that/should/never/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent runDir, got nil")
	}
	if out != nil || errLog != nil {
		t.Errorf("expected both nil on error, got out=%v err=%v", out, errLog)
	}
}
