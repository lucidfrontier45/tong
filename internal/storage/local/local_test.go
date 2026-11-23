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
	"sync"
	"sync/atomic"
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

func TestDriverDoesNotDeleteMetadataPreservingReplacement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	name := filepath.Join(root, "a.txt")
	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := local.New()
	dest, err := driver.ResolveDest(context.Background(), storage.Location{Scheme: storage.SchemeFile, Path: root}, false)
	if err != nil {
		t.Fatal(err)
	}
	info := listOne(t, dest.Endpoint, "a.txt")
	mtime := info.ModTime
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, name); err != nil {
		t.Fatal(err)
	}
	err = dest.Endpoint.(storage.ConditionalDeleter).DeleteIfUnchanged(context.Background(), "a.txt", info)
	if !errors.Is(err, storage.ErrChanged) {
		t.Errorf("DeleteIfUnchanged() = %v, want ErrChanged", err)
	}
	data, err := os.ReadFile(name) //nolint:gosec // name is inside the test's temporary directory
	if err != nil || string(data) != "new" {
		t.Errorf("replacement contents = %q, %v; want new file retained", data, err)
	}
}

func TestDriverIdentifiesAFileIndependentlyOfItsSpelling(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	driver := local.New()
	dest, err := driver.ResolveDest(t.Context(), storage.Location{
		Scheme: storage.SchemeFile,
		Path:   root,
	}, false)
	if err != nil {
		t.Fatalf("ResolveDest() = %v", err)
	}
	identifier := dest.Endpoint.(storage.Identifier)

	if err := os.WriteFile(filepath.Join(root, "Report"), []byte("body"), 0o600); err != nil {
		t.Fatalf("cannot write the file: %v", err)
	}
	report, err := identifier.Identity(t.Context(), "Report")
	if err != nil {
		t.Fatalf("Identity() = %v", err)
	}

	// Two hard links are one file, so they share an identity.
	if err := os.Link(filepath.Join(root, "Report"), filepath.Join(root, "link")); err != nil {
		t.Fatalf("cannot create a hard link: %v", err)
	}
	linked, err := identifier.Identity(t.Context(), "link")
	if err != nil {
		t.Fatalf("Identity() = %v", err)
	}
	if linked != report {
		t.Errorf("the identity of a hard link is %q, want %q", linked, report)
	}

	// A file reached under a different spelling of its name holds one
	// identity on a filesystem that folds the spellings together.
	if _, err := os.Lstat(filepath.Join(root, "REPORT")); err != nil {
		t.Skipf("the filesystem distinguishes the spellings: %v", err)
	}
	folded, err := identifier.Identity(t.Context(), "REPORT")
	if err != nil {
		t.Fatalf("Identity() = %v", err)
	}
	if folded != report {
		t.Errorf("the identity of the folded spelling is %q, want %q", folded, report)
	}

	// A different file does not share the identity.
	if err := os.WriteFile(filepath.Join(root, "other"), []byte("body"), 0o600); err != nil {
		t.Fatalf("cannot write the file: %v", err)
	}
	other, err := identifier.Identity(t.Context(), "other")
	if err != nil {
		t.Fatalf("Identity() = %v", err)
	}
	if other == report {
		t.Error("two different files share an identity")
	}
}

func TestDriverNeverDeletesAReplacementWhileChecking(t *testing.T) {
	t.Parallel()

	stamp := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for range 200 {
		root := t.TempDir()
		name := filepath.Join(root, "a.txt")
		if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
			t.Fatalf("cannot write the file: %v", err)
		}
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatalf("cannot set the time: %v", err)
		}
		// The replacement has the same length and time, so only the
		// identity of the file tells it apart.
		tmp := filepath.Join(root, ".swap")
		if err := os.WriteFile(tmp, []byte("new"), 0o600); err != nil {
			t.Fatalf("cannot write the replacement: %v", err)
		}
		if err := os.Chtimes(tmp, stamp, stamp); err != nil {
			t.Fatalf("cannot set the time: %v", err)
		}

		driver := local.New()
		dest, err := driver.ResolveDest(t.Context(), storage.Location{
			Scheme: storage.SchemeFile,
			Path:   root,
		}, false)
		if err != nil {
			t.Fatalf("ResolveDest() = %v", err)
		}
		info := listOne(t, dest.Endpoint, "a.txt")

		// A producer renames the replacement onto the checked file while
		// the check is running, so the removal races the replacement.
		var installed atomic.Bool
		started := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			<-started
			if err := os.Rename(tmp, name); err == nil {
				installed.Store(true)
			}
		})

		close(started)
		err = dest.Endpoint.(storage.ConditionalDeleter).
			DeleteIfUnchanged(t.Context(), "a.txt", info)
		wg.Wait()

		if err != nil && !errors.Is(err, storage.ErrChanged) {
			t.Fatalf("DeleteIfUnchanged() = %v, want nil or %v", err, storage.ErrChanged)
		}
		if !installed.Load() {
			// The producer did not land its replacement, so this round
			// proves nothing.
			continue
		}
		// Whatever the outcome, the replacement of the producer must still
		// be there. A removal that deletes it loses data.
		data, readErr := os.ReadFile(name) //nolint:gosec // the test wrote the file
		if readErr != nil {
			t.Fatalf("the replacement of the producer is gone: %v", readErr)
		}
		if string(data) != "new" {
			t.Fatalf("the file holds %q, want the replacement to survive", data)
		}
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
