package syncengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucidfrontier45/tong/internal/filter"
	"github.com/lucidfrontier45/tong/internal/storage"
	"github.com/lucidfrontier45/tong/internal/storage/local"
	"github.com/lucidfrontier45/tong/internal/storage/memory"
)

func TestRunCopiesTree(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "world")

	out := &bytes.Buffer{}
	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		Options{Recursive: true, Times: true},
		out, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d (%s)", code, CodeOK, out)
	}

	if got := readFile(t, filepath.Join(dst, "a.txt")); got != "hello" {
		t.Errorf("a.txt = %q, want %q", got, "hello")
	}
	if got := readFile(t, filepath.Join(dst, "sub", "b.txt")); got != "world" {
		t.Errorf("sub/b.txt = %q, want %q", got, "world")
	}
}

func TestRunKeepsModificationTimes(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	if err := os.Chtimes(filepath.Join(src, "a.txt"), modTime, modTime); err != nil {
		t.Fatalf("cannot set the modification time: %v", err)
	}

	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		Options{Recursive: true, Times: true},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	info, err := os.Stat(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatalf("cannot stat the copy: %v", err)
	}
	if !info.ModTime().Truncate(time.Second).Equal(modTime) {
		t.Errorf("modification time = %v, want %v", info.ModTime(), modTime)
	}
}

func TestRunSkipsUnchangedFiles(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")

	opts := Options{Recursive: true, Times: true}
	if code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		opts,
		io.Discard, io.Discard,
	); code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	out := &bytes.Buffer{}
	if code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		opts,
		out, io.Discard,
	); code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}
	if out.Len() != 0 {
		t.Errorf("the second run reported %q, want nothing", out)
	}
}

func TestRunIsNotRecursiveByDefault(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "world")

	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		Options{},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if got := readFile(t, filepath.Join(dst, "a.txt")); got != "hello" {
		t.Errorf("a.txt = %q, want %q", got, "hello")
	}
	if _, err := os.Stat(filepath.Join(dst, "sub", "b.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("sub/b.txt exists, want it to be left alone")
	}
}

func TestRunCopiesTheDirectoryItselfWithoutATrailingSeparator(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")

	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src)},
		resolveDest(t, dst),
		Options{Recursive: true},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if got := readFile(t, filepath.Join(dst, filepath.Base(src), "a.txt")); got != "hello" {
		t.Errorf("a.txt = %q, want %q", got, "hello")
	}
}

func TestRunDeletesExtraneousFiles(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	writeFile(t, filepath.Join(dst, "old.txt"), "old")

	out := &bytes.Buffer{}
	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		Options{Recursive: true, Delete: true},
		out, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d (%s)", code, CodeOK, out)
	}

	if _, err := os.Stat(filepath.Join(dst, "old.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old.txt exists, want it to be deleted")
	}
	if got := readFile(t, filepath.Join(dst, "a.txt")); got != "hello" {
		t.Errorf("a.txt = %q, want %q", got, "hello")
	}
}

func TestRunKeepsExcludedFilesWhenDeleting(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	writeFile(t, filepath.Join(dst, "keep.log"), "log")

	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		Options{
			Recursive: true,
			Delete:    true,
			Rules:     []filter.Rule{{Kind: filter.Exclude, Pattern: "*.log"}},
		},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if _, err := os.Stat(filepath.Join(dst, "keep.log")); err != nil {
		t.Errorf("keep.log was deleted, want it to be protected")
	}
}

func TestRunAppliesFilters(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	writeFile(t, filepath.Join(src, "b.log"), "log")

	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		Options{
			Recursive: true,
			Rules:     []filter.Rule{{Kind: filter.Exclude, Pattern: "*.log"}},
		},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if _, err := os.Stat(filepath.Join(dst, "b.log")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("b.log was copied, want it to be filtered out")
	}
}

func TestRunAppliesSizeLimits(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "small.txt"), "small")
	writeFile(t, filepath.Join(src, "big.txt"), "0123456789")

	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		Options{Recursive: true, MaxSize: 5},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if _, err := os.Stat(filepath.Join(dst, "big.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("big.txt was copied, want it to be skipped")
	}
	if got := readFile(t, filepath.Join(dst, "small.txt")); got != "small" {
		t.Errorf("small.txt = %q, want %q", got, "small")
	}
}

func TestRunDryRunChangesNothing(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")

	out := &bytes.Buffer{}
	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		Options{Recursive: true, DryRun: true},
		out, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if !strings.Contains(out.String(), "would copy") {
		t.Errorf("the plan is %q, want it to mention the copy", out)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a.txt was copied, want the run to be a dry run")
	}
}

func TestRunCopiesASingleFileOntoTheDestination(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")

	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, filepath.Join(src, "a.txt"))},
		resolveDestFile(t, filepath.Join(dst, "b.txt"), true),
		Options{},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if got := readFile(t, filepath.Join(dst, "b.txt")); got != "hello" {
		t.Errorf("b.txt = %q, want %q", got, "hello")
	}
}

func TestRunKeepsTheDestinationWhenItDoesNotRecurse(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "hello")
	writeFile(t, filepath.Join(dst, "extra.txt"), "extra")
	writeFile(t, filepath.Join(dst, "sub", "nested.txt"), "nested")

	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src+"/")},
		resolveDest(t, dst),
		Options{Delete: true},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	// A sync that does not recurse does not know what the destination
	// should hold, so nothing may be deleted.
	if _, err := os.Stat(filepath.Join(dst, "extra.txt")); err != nil {
		t.Error("extra.txt was deleted, want it to be kept")
	}
	if _, err := os.Stat(filepath.Join(dst, "sub", "nested.txt")); err != nil {
		t.Error("sub/nested.txt was deleted, want it to be kept")
	}
}

func TestRunMatchesTheFiltersAtTheTransferRoot(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	writeFile(t, filepath.Join(src, "top.txt"), "top")
	writeFile(t, filepath.Join(src, "sub", "nested.txt"), "nested")

	base := filepath.Base(src)
	code := Run(context.Background(),
		[]storage.Resolved{resolveSource(t, src)},
		resolveDest(t, dst),
		Options{
			Recursive: true,
			Rules:     []filter.Rule{{Kind: filter.Exclude, Pattern: base + "/sub"}},
		},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if _, err := os.Stat(filepath.Join(dst, base, "top.txt")); err != nil {
		t.Errorf("top.txt was not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, base, "sub", "nested.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("sub/nested.txt was copied, want it to be filtered out")
	}
}

func TestRunWritesTheDiagnosticsToStderr(t *testing.T) {
	t.Parallel()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(context.Background(),
		[]storage.Resolved{{Endpoint: &brokenEndpoint{}, IsDir: true, Display: "src"}},
		storage.Resolved{Endpoint: &brokenEndpoint{}, IsDir: true, Display: "dst"},
		Options{},
		stdout, stderr,
	)
	if code != CodePartial {
		t.Fatalf("Run() = %d, want %d", code, CodePartial)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want it to be empty", stdout)
	}
	if !strings.Contains(stderr.String(), "cannot read") {
		t.Errorf("stderr = %q, want it to hold the diagnostic", stderr)
	}
}

func TestRunReportsFilesThatAreGone(t *testing.T) {
	t.Parallel()

	src := &goneEndpoint{files: map[string][]byte{"a.txt": []byte("hello")}}
	dest := &goneEndpoint{files: map[string][]byte{}}

	code := Run(context.Background(),
		[]storage.Resolved{{Endpoint: src, IsDir: true, Display: "src"}},
		storage.Resolved{Endpoint: dest, IsDir: true, Display: "dst"},
		Options{Recursive: true},
		io.Discard, io.Discard,
	)
	if code != CodeVanished {
		t.Fatalf("Run() = %d, want %d", code, CodeVanished)
	}
}

func TestRunReportsVanishedFiles(t *testing.T) {
	t.Parallel()

	src := &vanishingEndpoint{files: map[string][]byte{"a.txt": []byte("hello")}}
	dest := &vanishingEndpoint{files: map[string][]byte{}}

	code := Run(context.Background(),
		[]storage.Resolved{{Endpoint: src, IsDir: true, Display: "src"}},
		storage.Resolved{Endpoint: dest, IsDir: true, Display: "dst"},
		Options{Recursive: true},
		io.Discard, io.Discard,
	)
	if code != CodeVanished {
		t.Fatalf("Run() = %d, want %d", code, CodeVanished)
	}
}

func TestRunWithTheMemoryDriver(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte("hello"), time.Now())
	driver.Put("src", "sub/b.txt", []byte("world"), time.Now())

	src, err := driver.ResolveSource(context.Background(), storage.Location{
		Scheme:   storage.SchemeFile,
		Path:     "src/",
		Trailing: true,
	})
	if err != nil {
		t.Fatalf("ResolveSource() = %v", err)
	}
	dest, err := driver.ResolveDest(context.Background(), storage.Location{
		Scheme: storage.SchemeFile,
		Path:   "dst",
	}, false)
	if err != nil {
		t.Fatalf("ResolveDest() = %v", err)
	}

	code := Run(context.Background(),
		[]storage.Resolved{src},
		dest,
		Options{Recursive: true},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if got, ok := driver.Get("dst", "a.txt"); !ok || string(got) != "hello" {
		t.Errorf("a.txt = %q, want %q", got, "hello")
	}
	if got, ok := driver.Get("dst", "sub/b.txt"); !ok || string(got) != "world" {
		t.Errorf("sub/b.txt = %q, want %q", got, "world")
	}
}

// resolveSource resolves a local source location.
func resolveSource(t *testing.T, raw string) storage.Resolved {
	t.Helper()

	loc, err := storage.ParseLocation(raw)
	if err != nil {
		t.Fatalf("ParseLocation(%q) = %v", raw, err)
	}
	resolved, err := local.New().ResolveSource(context.Background(), loc)
	if err != nil {
		t.Fatalf("ResolveSource(%q) = %v", raw, err)
	}
	return resolved
}

// resolveDest resolves a local destination location.
func resolveDest(t *testing.T, raw string) storage.Resolved {
	t.Helper()

	return resolveDestFile(t, raw, false)
}

// resolveDestFile resolves a local destination location that may hold a
// single file.
func resolveDestFile(t *testing.T, raw string, singleFile bool) storage.Resolved {
	t.Helper()

	loc, err := storage.ParseLocation(raw)
	if err != nil {
		t.Fatalf("ParseLocation(%q) = %v", raw, err)
	}
	resolved, err := local.New().ResolveDest(context.Background(), loc, singleFile)
	if err != nil {
		t.Fatalf("ResolveDest(%q) = %v", raw, err)
	}
	return resolved
}

// writeFile writes a file for the tests.
func writeFile(t *testing.T, name, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatalf("cannot create %s: %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatalf("cannot write %s: %v", name, err)
	}
}

// readFile reads a file for the tests.
func readFile(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(name) //nolint:gosec // the tests write the file
	if err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(data)
}

// brokenEndpoint is an endpoint that cannot be listed.
type brokenEndpoint struct{}

// List always fails.
func (e *brokenEndpoint) List(_ context.Context, _ string, _ func(storage.FileInfo) error) error {
	return errors.New("the endpoint is broken")
}

// Open always fails.
func (e *brokenEndpoint) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, errors.New("the endpoint is broken")
}

// Create always fails.
func (e *brokenEndpoint) Create(_ context.Context, _ string, _ time.Time, _ io.Reader) error {
	return errors.New("the endpoint is broken")
}

// Delete always fails.
func (e *brokenEndpoint) Delete(_ context.Context, _ string) error {
	return errors.New("the endpoint is broken")
}

// goneEndpoint is an endpoint whose files are gone, the way an object that
// was deleted in the bucket reports it.
type goneEndpoint struct {
	files map[string][]byte
}

// List calls fn for every file.
func (e *goneEndpoint) List(_ context.Context, prefix string, fn func(storage.FileInfo) error) error {
	for key, data := range e.files {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if err := fn(storage.FileInfo{Key: key, Size: int64(len(data))}); err != nil {
			return err
		}
	}
	return nil
}

// Open reports that the file is gone.
func (e *goneEndpoint) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("cannot read the object: %w", storage.ErrNotFound)
}

// Create writes a file.
func (e *goneEndpoint) Create(_ context.Context, key string, _ time.Time, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	e.files[key] = data
	return nil
}

// Delete removes a file.
func (e *goneEndpoint) Delete(_ context.Context, key string) error {
	delete(e.files, key)
	return nil
}

// vanishingEndpoint is an endpoint whose files disappear before they can be
// read.
type vanishingEndpoint struct {
	files map[string][]byte
}

// List calls fn for every file.
func (e *vanishingEndpoint) List(_ context.Context, prefix string, fn func(storage.FileInfo) error) error {
	for key, data := range e.files {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if err := fn(storage.FileInfo{Key: key, Size: int64(len(data))}); err != nil {
			return err
		}
	}
	return nil
}

// Open always reports that the file is gone.
func (e *vanishingEndpoint) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, fs.ErrNotExist
}

// Create writes a file.
func (e *vanishingEndpoint) Create(_ context.Context, key string, _ time.Time, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	e.files[key] = data
	return nil
}

// Delete removes a file.
func (e *vanishingEndpoint) Delete(_ context.Context, key string) error {
	delete(e.files, key)
	return nil
}
