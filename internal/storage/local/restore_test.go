package local

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucidfrontier45/tong/internal/storage"
)

func TestRestoreNeverOverwritesNewArrival(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	staged, full := filepath.Join(dir, "staged"), filepath.Join(dir, "source")
	for _, name := range []string{staged, full} {
		if err := os.WriteFile(name, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := restoreChanged(staged, full)
	if !errors.Is(err, storage.ErrChanged) || !strings.Contains(err.Error(), staged) {
		t.Fatalf("restore error %v does not identify the retained replacement", err)
	}
	for _, name := range []string{staged, full} {
		data, err := os.ReadFile(name) //nolint:gosec // paths in the test directory
		if err != nil || string(data) != name {
			t.Fatalf("read %s = %q, %v", name, data, err)
		}
	}
}
