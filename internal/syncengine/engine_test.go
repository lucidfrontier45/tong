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

func TestRunComparesThePreservedTimesWhenItUpdates(t *testing.T) {
	t.Parallel()

	uploaded := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	preserved := time.Date(2022, time.January, 1, 0, 0, 0, 0, time.UTC)
	older := time.Date(2021, time.January, 1, 0, 0, 0, 0, time.UTC)

	// The source was uploaded before the destination was written, but the
	// modification time it preserved is the newer one.
	source := &objectEndpoint{
		files:    map[string][]byte{"a.txt": []byte("new content")},
		modTimes: map[string]time.Time{"a.txt": preserved},
		uploaded: uploaded,
	}
	dest := &objectEndpoint{
		files:    map[string][]byte{"a.txt": []byte("old")},
		modTimes: map[string]time.Time{"a.txt": older},
		uploaded: uploaded,
	}

	code := Run(t.Context(),
		[]storage.Resolved{{Endpoint: source, IsDir: true, Display: "s3://bucket/src/"}},
		storage.Resolved{Endpoint: dest, IsDir: true, Display: "s3://bucket/dst/"},
		Options{Recursive: true, Update: true},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	if got := string(dest.files["a.txt"]); got != "new content" {
		t.Errorf("a.txt = %q, want %q", got, "new content")
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

func TestRunTransfersInParallel(t *testing.T) {
	t.Parallel()

	const files = 40

	driver := memory.New()
	for i := range files {
		key := fmt.Sprintf("file-%02d.txt", i)
		driver.Put("src", key, []byte(strings.Repeat("x", 1024+i)), time.Now())
	}

	code := Run(t.Context(),
		[]storage.Resolved{memorySource(t, driver, "src/")},
		memoryDest(t, driver, "dst"),
		Options{Recursive: true, Parallel: 8},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}

	keys := driver.Keys("dst")
	if len(keys) != files {
		t.Fatalf("the destination holds %d files, want %d", len(keys), files)
	}
	for i := range files {
		key := fmt.Sprintf("file-%02d.txt", i)
		got, ok := driver.Get("dst", key)
		if !ok {
			t.Errorf("%s was not copied", key)
			continue
		}
		if want := strings.Repeat("x", 1024+i); string(got) != want {
			t.Errorf("%s holds %d bytes, want %d", key, len(got), len(want))
		}
	}
}

func TestRunRemoveSourceFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// source is the content of the source file.
		source string
		// dest is the content of the destination file. An empty string
		// means that the destination does not hold the file.
		dest string
		// destTime is the modification time of the destination file.
		destTime time.Time
		opts     Options
		// wantSource is what is left of the source file.
		wantSource string
		// wantDest is what the destination holds.
		wantDest string
		wantCode int
	}{
		{
			name:       "a transferred file is removed",
			source:     "hello",
			dest:       "",
			wantSource: "",
			wantDest:   "hello",
			wantCode:   CodeOK,
		},
		{
			name:       "a file that is already in sync is removed",
			source:     "hello",
			dest:       "hello",
			wantSource: "",
			wantDest:   "hello",
			wantCode:   CodeOK,
		},
		{
			name:       "--existing keeps a file that the destination does not have",
			source:     "hello",
			opts:       Options{Existing: true},
			wantSource: "hello",
			wantDest:   "",
			wantCode:   CodeOK,
		},
		{
			name:       "--ignore-existing keeps a file that the destination has",
			source:     "hello",
			dest:       "hello",
			opts:       Options{IgnoreExisting: true},
			wantSource: "hello",
			wantDest:   "hello",
			wantCode:   CodeOK,
		},
		{
			name:       "--update keeps a file that is newer on the destination",
			source:     "hello",
			dest:       "world",
			destTime:   time.Now(),
			opts:       Options{Update: true},
			wantSource: "hello",
			wantDest:   "world",
			wantCode:   CodeOK,
		},
		{
			name:       "a dry run removes nothing",
			source:     "hello",
			opts:       Options{DryRun: true},
			wantSource: "hello",
			wantDest:   "",
			wantCode:   CodeOK,
		},
		{
			name:       "a filtered file is not removed",
			source:     "hello",
			opts:       Options{Rules: []filter.Rule{{Kind: filter.Exclude, Pattern: "*.txt"}}},
			wantSource: "hello",
			wantDest:   "",
			wantCode:   CodeOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			driver := memory.New()
			driver.Put("src", "a.txt", []byte(tc.source), time.Now().Add(-time.Hour))
			if tc.dest != "" {
				driver.Put("dst", "a.txt", []byte(tc.dest), tc.destTime)
			}

			opts := tc.opts
			opts.Recursive = true
			opts.RemoveSourceFiles = true

			code := Run(t.Context(),
				[]storage.Resolved{memorySource(t, driver, "src/")},
				memoryDest(t, driver, "dst"),
				opts,
				io.Discard, io.Discard,
			)
			if code != tc.wantCode {
				t.Errorf("Run() = %d, want %d", code, tc.wantCode)
			}

			got, _ := driver.Get("src", "a.txt")
			if string(got) != tc.wantSource {
				t.Errorf("the source holds %q, want %q", got, tc.wantSource)
			}
			got, _ = driver.Get("dst", "a.txt")
			if string(got) != tc.wantDest {
				t.Errorf("the destination holds %q, want %q", got, tc.wantDest)
			}
		})
	}
}

func TestRunKeepsTheSourceWhenTheTransferFails(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte("hello"), time.Now())

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(t.Context(),
		[]storage.Resolved{memorySource(t, driver, "src/")},
		storage.Resolved{Endpoint: &failingDestEndpoint{}, IsDir: true, Display: "dst"},
		Options{Recursive: true, RemoveSourceFiles: true, Stats: true},
		stdout, stderr,
	)
	if code != CodePartial {
		t.Fatalf("Run() = %d, want %d", code, CodePartial)
	}
	if _, ok := driver.Get("src", "a.txt"); !ok {
		t.Error("the source file was removed, want it to be kept")
	}
	if !strings.Contains(stdout.String(), "errors=1\n") {
		t.Errorf("the summary is %q, want it to count the failure", stdout)
	}
}

func TestRunPrintsStats(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte("hello"), time.Now())
	driver.Put("src", "b.txt", []byte("world"), time.Now())
	driver.Put("dst", "a.txt", []byte("hello"), time.Now())
	driver.Put("dst", "old.txt", []byte("old"), time.Now())

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(t.Context(),
		[]storage.Resolved{memorySource(t, driver, "src/")},
		memoryDest(t, driver, "dst"),
		Options{Recursive: true, Delete: true, RemoveSourceFiles: true, Stats: true},
		stdout, stderr,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d (%s)", code, CodeOK, stderr)
	}

	want := "files_total=2\n" +
		"files_transferred=1\n" +
		"files_skipped=1\n" +
		"files_deleted=1\n" +
		"files_removed=2\n" +
		"bytes_transferred=5\n" +
		"errors=0\n"
	if got := stdout.String(); got != want {
		t.Errorf("the summary is\n%s\nwant\n%s", got, want)
	}
}

func TestRunPrintsStatsOnADryRun(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte("hello"), time.Now())
	driver.Put("src", "b.txt", []byte("world"), time.Now())
	driver.Put("dst", "a.txt", []byte("hello"), time.Now())

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(t.Context(),
		[]storage.Resolved{memorySource(t, driver, "src/")},
		memoryDest(t, driver, "dst"),
		Options{Recursive: true, DryRun: true, RemoveSourceFiles: true, Stats: true},
		stdout, stderr,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d (%s)", code, CodeOK, stderr)
	}

	want := "would copy src/b.txt -> dst/b.txt\n" +
		"would remove src/b.txt\n" +
		"would remove src/a.txt\n" +
		"files_total=2\n" +
		"files_transferred=0\n" +
		"files_skipped=1\n" +
		"files_deleted=0\n" +
		"files_removed=0\n" +
		"bytes_transferred=0\n" +
		"errors=0\n"
	if got := stdout.String(); got != want {
		t.Errorf("the summary is\n%s\nwant\n%s", got, want)
	}
}

func TestRunWithABandwidthLimit(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte(strings.Repeat("x", 8*1024)), time.Now())

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(t.Context(),
		[]storage.Resolved{memorySource(t, driver, "src/")},
		memoryDest(t, driver, "dst"),
		Options{Recursive: true, BWLimit: 1 << 20},
		stdout, stderr,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d (%s)", code, CodeOK, stderr)
	}
	if got, ok := driver.Get("dst", "a.txt"); !ok || len(got) != 8*1024 {
		t.Errorf("the destination holds %d bytes, want %d", len(got), 8*1024)
	}
}

func TestRunTellsTheDestinationTheSize(t *testing.T) {
	t.Parallel()

	const content = "hello world"

	driver := memory.New()
	driver.Put("src", "a.txt", []byte(content), time.Now())
	dest := memoryDest(t, driver, "dst")
	sized := &sizedEndpoint{Endpoint: dest.Endpoint}

	code := Run(t.Context(),
		[]storage.Resolved{memorySource(t, driver, "src/")},
		storage.Resolved{Endpoint: sized, IsDir: true, Display: dest.Display},
		Options{Recursive: true},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}
	if sized.size != int64(len(content)) {
		t.Errorf("the destination was told %d bytes, want %d", sized.size, len(content))
	}
	if got, ok := driver.Get("dst", "a.txt"); !ok || string(got) != content {
		t.Errorf("a.txt = %q, want %q", got, content)
	}
}

// sizedEndpoint is a destination that records the size it is told.
type sizedEndpoint struct {
	storage.Endpoint
	size int64
}

// CreateSized records the size and writes the file.
func (e *sizedEndpoint) CreateSized(ctx context.Context, key string, modTime time.Time, size int64, r io.Reader) error {
	e.size = size
	return e.Create(ctx, key, modTime, r)
}

// failingDestEndpoint lists files but cannot write or delete them.
type failingDestEndpoint struct{}

// List reports that the destination is empty.
func (e *failingDestEndpoint) List(_ context.Context, _ string, _ func(storage.FileInfo) error) error {
	return nil
}

// Open always fails.
func (e *failingDestEndpoint) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, errors.New("cannot open")
}

// Create always fails.
func (e *failingDestEndpoint) Create(_ context.Context, _ string, _ time.Time, _ io.Reader) error {
	return errors.New("cannot write")
}

// Delete always fails.
func (e *failingDestEndpoint) Delete(_ context.Context, _ string) error {
	return errors.New("cannot delete")
}

// memorySource resolves an in-memory source location.
func memorySource(t *testing.T, driver *memory.Driver, raw string) storage.Resolved {
	t.Helper()

	loc, err := storage.ParseLocation(raw)
	if err != nil {
		t.Fatalf("ParseLocation(%q) = %v", raw, err)
	}
	resolved, err := driver.ResolveSource(t.Context(), loc)
	if err != nil {
		t.Fatalf("ResolveSource(%q) = %v", raw, err)
	}
	return resolved
}

// memoryDest resolves an in-memory destination location.
func memoryDest(t *testing.T, driver *memory.Driver, raw string) storage.Resolved {
	t.Helper()

	loc, err := storage.ParseLocation(raw)
	if err != nil {
		t.Fatalf("ParseLocation(%q) = %v", raw, err)
	}
	resolved, err := driver.ResolveDest(t.Context(), loc, false)
	if err != nil {
		t.Fatalf("ResolveDest(%q) = %v", raw, err)
	}
	return resolved
}

// objectEndpoint mimics an object store: it reports the time a file was
// uploaded, and the modification time that was preserved with it once the
// caller asks for the times.
type objectEndpoint struct {
	files map[string][]byte
	// modTimes holds the modification times that were preserved with the
	// files.
	modTimes map[string]time.Time
	// uploaded is the time every file was uploaded at.
	uploaded time.Time
	// useModTime tells whether the preserved times are reported.
	useModTime bool
}

// UseModTime tells the endpoint whether the modification times of the
// listed files matter.
func (e *objectEndpoint) UseModTime(enabled bool) {
	e.useModTime = enabled
}

// List calls fn for every file.
func (e *objectEndpoint) List(_ context.Context, prefix string, fn func(storage.FileInfo) error) error {
	for key, data := range e.files {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		info := storage.FileInfo{Key: key, Size: int64(len(data)), ModTime: e.uploaded}
		if e.useModTime {
			if modTime, ok := e.modTimes[key]; ok {
				info.ModTime = modTime
			}
		}
		if err := fn(info); err != nil {
			return err
		}
	}
	return nil
}

// Open returns the contents of a file.
func (e *objectEndpoint) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := e.files[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Create writes a file.
func (e *objectEndpoint) Create(_ context.Context, key string, modTime time.Time, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	e.files[key] = data
	if !modTime.IsZero() {
		e.modTimes[key] = modTime
	}
	return nil
}

// Delete removes a file.
func (e *objectEndpoint) Delete(_ context.Context, key string) error {
	delete(e.files, key)
	return nil
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

func TestRunRefusesToRemoveTheDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	name := filepath.Join(dir, "a.txt")
	writeFile(t, name, "hello")

	stderr := &bytes.Buffer{}
	code := Run(t.Context(),
		[]storage.Resolved{resolveSource(t, name)},
		resolveDestFile(t, name, true),
		Options{RemoveSourceFiles: true},
		io.Discard, stderr,
	)
	if code != CodePartial {
		t.Fatalf("Run() = %d, want %d", code, CodePartial)
	}
	if got := readFile(t, name); got != "hello" {
		t.Errorf("the destination holds %q, want %q", got, "hello")
	}
	if !strings.Contains(stderr.String(), "skipping remove of destination file") {
		t.Errorf("the diagnostics are %q, want the refusal", stderr)
	}
}

func TestRunRefusesToRemoveADirectoryIntoItself(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	name := filepath.Join(dir, "a.txt")
	writeFile(t, name, "hello")

	code := Run(t.Context(),
		[]storage.Resolved{resolveSource(t, dir+"/")},
		resolveDest(t, dir),
		Options{Recursive: true, RemoveSourceFiles: true},
		io.Discard, io.Discard,
	)
	if code != CodePartial {
		t.Fatalf("Run() = %d, want %d", code, CodePartial)
	}
	if got := readFile(t, name); got != "hello" {
		t.Errorf("the file holds %q, want it to be kept", got)
	}
}

func TestRunRefusesToRemoveThroughASymlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	name := filepath.Join(dir, "data", "a.txt")
	writeFile(t, name, "hello")
	if err := os.Symlink(filepath.Join(dir, "data"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}

	code := Run(t.Context(),
		[]storage.Resolved{resolveSource(t, filepath.Join(dir, "data")+"/")},
		resolveDest(t, filepath.Join(dir, "link")+"/"),
		Options{Recursive: true, RemoveSourceFiles: true},
		io.Discard, io.Discard,
	)
	if code != CodePartial {
		t.Fatalf("Run() = %d, want %d", code, CodePartial)
	}
	if got := readFile(t, name); got != "hello" {
		t.Errorf("the file holds %q, want it to be kept", got)
	}
}

func TestRunRefusesToMoveWhenTwoSourcesWriteOneKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "x"), "AAAA")
	writeFile(t, filepath.Join(dir, "b", "x"), "BBBBBBBB")
	dest := filepath.Join(dir, "dst")

	code := Run(t.Context(),
		[]storage.Resolved{
			resolveSource(t, filepath.Join(dir, "a")+"/"),
			resolveSource(t, filepath.Join(dir, "b")+"/"),
		},
		resolveDest(t, dest),
		Options{Recursive: true, RemoveSourceFiles: true},
		io.Discard, io.Discard,
	)
	if code != CodeUsage {
		t.Fatalf("Run() = %d, want %d", code, CodeUsage)
	}
	if got := readFile(t, filepath.Join(dir, "a", "x")); got != "AAAA" {
		t.Errorf("a/x = %q, want it to be kept", got)
	}
	if got := readFile(t, filepath.Join(dir, "b", "x")); got != "BBBBBBBB" {
		t.Errorf("b/x = %q, want it to be kept", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "x")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the destination was written, want nothing to happen")
	}
}

func TestRunLetsTheFirstSourceWinACopyCollision(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "x"), "AAAA")
	writeFile(t, filepath.Join(dir, "b", "x"), "BBBBBBBB")
	dest := filepath.Join(dir, "dst")

	code := Run(t.Context(),
		[]storage.Resolved{
			resolveSource(t, filepath.Join(dir, "a")+"/"),
			resolveSource(t, filepath.Join(dir, "b")+"/"),
		},
		resolveDest(t, dest),
		Options{Recursive: true},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}
	if got := readFile(t, filepath.Join(dest, "x")); got != "AAAA" {
		t.Errorf("dst/x = %q, want the first source to win", got)
	}
}

func TestRunComparesModificationTimesInAMove(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte("aaa"), time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	driver.Put("dst", "a.txt", []byte("bbb"), time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))

	code := Run(t.Context(),
		[]storage.Resolved{memorySource(t, driver, "src/")},
		memoryDest(t, driver, "dst"),
		Options{Recursive: true, RemoveSourceFiles: true},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}
	if got, _ := driver.Get("dst", "a.txt"); string(got) != "aaa" {
		t.Errorf("dst holds %q, want the source to replace it", got)
	}
	if keys := driver.Keys("src"); len(keys) != 0 {
		t.Errorf("the source holds %v, want it to be removed", keys)
	}
}

func TestRunKeepsTheCopyOnlySizeShortcut(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte("aaa"), time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	driver.Put("dst", "a.txt", []byte("bbb"), time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))

	code := Run(t.Context(),
		[]storage.Resolved{memorySource(t, driver, "src/")},
		memoryDest(t, driver, "dst"),
		Options{Recursive: true},
		io.Discard, io.Discard,
	)
	if code != CodeOK {
		t.Fatalf("Run() = %d, want %d", code, CodeOK)
	}
	if got, _ := driver.Get("dst", "a.txt"); string(got) != "bbb" {
		t.Errorf("dst holds %q, want the size-only shortcut to keep it", got)
	}
}

func TestRunKeepsAChangedSource(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte("hello"), time.Now())

	src := memorySource(t, driver, "src/")
	src.Endpoint = &changingEndpoint{Endpoint: src.Endpoint}

	stderr := &bytes.Buffer{}
	code := Run(t.Context(),
		[]storage.Resolved{src},
		memoryDest(t, driver, "dst"),
		Options{Recursive: true, RemoveSourceFiles: true},
		io.Discard, stderr,
	)
	if code != CodePartial {
		t.Fatalf("Run() = %d, want %d", code, CodePartial)
	}
	if keys := driver.Keys("src"); len(keys) != 1 {
		t.Errorf("the source holds %v, want it to be kept", keys)
	}
	if !strings.Contains(stderr.String(), "file changed since it was listed") {
		t.Errorf("the diagnostics are %q, want the change to be reported", stderr)
	}
}

func TestRunStopsRemovingWhenCancelled(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte("changed"), time.Now())
	driver.Put("src", "b.txt", []byte("same"), time.Now())
	driver.Put("dst", "b.txt", []byte("same"), time.Now())

	ctx, cancel := context.WithCancel(t.Context())
	dest := memoryDest(t, driver, "dst")
	dest.Endpoint = &cancellingEndpoint{Endpoint: dest.Endpoint, cancel: cancel}

	code := Run(ctx,
		[]storage.Resolved{memorySource(t, driver, "src/")},
		dest,
		Options{Recursive: true, RemoveSourceFiles: true},
		io.Discard, io.Discard,
	)
	if code != CodeSignal {
		t.Fatalf("Run() = %d, want %d", code, CodeSignal)
	}
	if keys := driver.Keys("src"); len(keys) != 2 {
		t.Errorf("the source holds %v, want no file to be removed after a cancel", keys)
	}
}

// changingEndpoint is a source whose files change after they were listed.
type changingEndpoint struct {
	storage.Endpoint
}

// DeleteIfUnchanged reports that the file changed.
func (e *changingEndpoint) DeleteIfUnchanged(_ context.Context, key string, _ storage.FileInfo) error {
	return fmt.Errorf("%w: %s", storage.ErrChanged, key)
}

// cancellingEndpoint is a destination that cancels the run on the first
// write.
type cancellingEndpoint struct {
	storage.Endpoint
	cancel context.CancelFunc
}

// Create cancels the run and then writes the file.
func (e *cancellingEndpoint) Create(ctx context.Context, key string, modTime time.Time, r io.Reader) error {
	e.cancel()
	return e.Endpoint.Create(ctx, key, modTime, r)
}
