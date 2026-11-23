package local_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucidfrontier45/tong/internal/storage"
	"github.com/lucidfrontier45/tong/internal/storage/local"
)

// failingReader fails after the first read.
type failingReader struct {
	read bool
}

// Read returns some data and then fails.
func (r *failingReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("the source cannot be read")
	}
	r.read = true
	return copy(p, []byte("new")), nil
}

func TestDriverResolvesDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatalf("cannot create the directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("cannot write the file: %v", err)
	}

	driver := local.New()
	source, err := driver.ResolveSource(context.Background(), storage.Location{
		Scheme:   storage.SchemeFile,
		Path:     root + "/",
		Trailing: true,
	})
	if err != nil {
		t.Fatalf("ResolveSource() = %v", err)
	}
	if !source.IsDir {
		t.Error("the source is not a directory")
	}
	if source.Name != "" {
		t.Errorf("the name is %q, want it to be empty", source.Name)
	}

	var keys []string
	if err := source.Endpoint.List(context.Background(), "", func(info storage.FileInfo) error {
		keys = append(keys, info.Key)
		return nil
	}); err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(keys) != 1 || keys[0] != "a.txt" {
		t.Errorf("the listing is %v, want [a.txt]", keys)
	}
}

func TestDriverListsOneLevel(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatalf("cannot create the directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("cannot write the file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("world"), 0o600); err != nil {
		t.Fatalf("cannot write the file: %v", err)
	}

	driver := local.New()
	source, err := driver.ResolveSource(context.Background(), storage.Location{
		Scheme:   storage.SchemeFile,
		Path:     root + "/",
		Trailing: true,
	})
	if err != nil {
		t.Fatalf("ResolveSource() = %v", err)
	}

	var keys []string
	if err := source.Endpoint.(storage.ShallowLister).ListShallow(context.Background(), "", func(info storage.FileInfo) error {
		keys = append(keys, info.Key)
		return nil
	}); err != nil {
		t.Fatalf("ListShallow() = %v", err)
	}
	if len(keys) != 1 || keys[0] != "a.txt" {
		t.Errorf("the listing is %v, want [a.txt]", keys)
	}
}

func TestDriverCreatesAndDeletesFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	driver := local.New()
	dest, err := driver.ResolveDest(context.Background(), storage.Location{
		Scheme: storage.SchemeFile,
		Path:   root,
	}, false)
	if err != nil {
		t.Fatalf("ResolveDest() = %v", err)
	}

	modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := dest.Endpoint.Create(context.Background(), "sub/a.txt", modTime, bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	info, err := os.Stat(filepath.Join(root, "sub", "a.txt"))
	if err != nil {
		t.Fatalf("cannot stat the file: %v", err)
	}
	if !info.ModTime().Truncate(time.Second).Equal(modTime) {
		t.Errorf("modification time = %v, want %v", info.ModTime(), modTime)
	}

	if err := dest.Endpoint.Delete(context.Background(), "sub/a.txt"); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "a.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the file still exists, want it to be deleted")
	}
}

func TestDriverKeepsThePreviousCopyWhenATransferFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	driver := local.New()
	dest, err := driver.ResolveDest(context.Background(), storage.Location{
		Scheme: storage.SchemeFile,
		Path:   root,
	}, false)
	if err != nil {
		t.Fatalf("ResolveDest() = %v", err)
	}

	if err := dest.Endpoint.Create(context.Background(), "a.txt", time.Time{}, bytes.NewReader([]byte("old"))); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if err := dest.Endpoint.Create(context.Background(), "a.txt", time.Time{}, &failingReader{}); err == nil {
		t.Fatal("Create() = nil, want the failed transfer to be reported")
	}

	got, err := os.ReadFile(filepath.Join(root, "a.txt")) //nolint:gosec // the test wrote the file
	if err != nil {
		t.Fatalf("cannot read the destination: %v", err)
	}
	if string(got) != "old" {
		t.Errorf("a.txt = %q, want the previous copy to be kept", got)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("cannot read the directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tong-") {
			t.Errorf("the temporary file %s was left behind", entry.Name())
		}
	}
}

func TestDriverKeepsKeysInsideTheRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	driver := local.New()
	dest, err := driver.ResolveDest(context.Background(), storage.Location{
		Scheme: storage.SchemeFile,
		Path:   root,
	}, false)
	if err != nil {
		t.Fatalf("ResolveDest() = %v", err)
	}

	if _, err := dest.Endpoint.Open(context.Background(), "../escape.txt"); err == nil {
		t.Error("Open() = nil, want the key to be refused")
	}
	if err := dest.Endpoint.Create(context.Background(), "../escape.txt", time.Time{}, bytes.NewReader(nil)); err == nil {
		t.Error("Create() = nil, want the key to be refused")
	}
}

func TestDriverResolvesARelativeSingleFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := os.WriteFile("a.txt", []byte("hello"), 0o600); err != nil {
		t.Fatalf("cannot write the file: %v", err)
	}

	resolved, err := local.New().ResolveSource(context.Background(), storage.Location{
		Scheme: storage.SchemeFile,
		Path:   "a.txt",
	})
	if err != nil {
		t.Fatalf("ResolveSource() = %v", err)
	}

	reader, err := resolved.Endpoint.Open(context.Background(), resolved.FileKey)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() = %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("the file holds %q, want %q", got, "hello")
	}
}

func TestDriverDeletesOnlyAnUnchangedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	name := filepath.Join(root, "a.txt")
	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatalf("cannot write the file: %v", err)
	}

	driver := local.New()
	dest, err := driver.ResolveDest(context.Background(), storage.Location{
		Scheme: storage.SchemeFile,
		Path:   root,
	}, false)
	if err != nil {
		t.Fatalf("ResolveDest() = %v", err)
	}

	info := listOne(t, dest.Endpoint, "a.txt")
	if err := dest.Endpoint.(storage.ConditionalDeleter).
		DeleteIfUnchanged(context.Background(), "a.txt", info); err != nil {
		t.Fatalf("DeleteIfUnchanged() = %v", err)
	}
	if _, err := os.Stat(name); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the unchanged file was kept, want it to be deleted")
	}

	// A file that changes after it was listed keeps its place.
	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatalf("cannot write the file: %v", err)
	}
	info = listOne(t, dest.Endpoint, "a.txt")
	if err := os.WriteFile(name, []byte("replaced"), 0o600); err != nil {
		t.Fatalf("cannot replace the file: %v", err)
	}
	err = dest.Endpoint.(storage.ConditionalDeleter).DeleteIfUnchanged(context.Background(), "a.txt", info)
	if !errors.Is(err, storage.ErrChanged) {
		t.Errorf("DeleteIfUnchanged() = %v, want %v", err, storage.ErrChanged)
	}
	if _, err := os.Stat(name); err != nil {
		t.Errorf("the changed file was removed: %v", err)
	}
}

// listOne returns the information of a single file.
func listOne(t *testing.T, endpoint storage.Endpoint, key string) storage.FileInfo {
	t.Helper()

	var found storage.FileInfo
	if err := endpoint.List(context.Background(), key, func(info storage.FileInfo) error {
		if info.Key == key {
			found = info
		}
		return nil
	}); err != nil {
		t.Fatalf("List() = %v", err)
	}
	return found
}
