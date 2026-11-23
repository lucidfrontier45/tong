package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveVolumeRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := filepath.VolumeName(dir) + string(filepath.Separator)
	check := func(t *testing.T, path string) {
		t.Helper()
		e, err := newEndpoint(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.resolve("child")
		if want := filepath.Join(root, "child"); err != nil || got != want {
			t.Fatalf("resolve(child) = %q, %v; want %q", got, err, want)
		}
	}
	check(t, root)
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(dir, "root")
		if err := os.Symlink(root, link); err != nil {
			t.Skipf("cannot create directory symlink: %v", err)
		}
		check(t, link)
	})
}

func TestResolveRejectsEscape(t *testing.T) {
	t.Parallel()
	e, err := newEndpoint(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.resolve("../sibling"); err == nil {
		t.Fatalf("resolved escaping key to %q", got)
	}
}
