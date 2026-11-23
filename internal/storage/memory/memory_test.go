package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lucidfrontier45/tong/internal/storage"
	"github.com/lucidfrontier45/tong/internal/storage/memory"
)

func TestDriverListsTheRootNamespace(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("", "a.txt", []byte("hello"), time.Now())

	dest, err := driver.ResolveDest(context.Background(), storage.Location{
		Scheme: storage.SchemeFile,
		Path:   "/",
	}, false)
	if err != nil {
		t.Fatalf("ResolveDest() = %v", err)
	}

	var keys []string
	if err := dest.Endpoint.List(context.Background(), "", func(info storage.FileInfo) error {
		keys = append(keys, info.Key)
		return nil
	}); err != nil {
		t.Fatalf("List() = %v", err)
	}

	if len(keys) != 1 || keys[0] != "a.txt" {
		t.Errorf("the listing is %v, want [a.txt]", keys)
	}
}

func TestDriverKeepsTheNamespacesApart(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	driver.Put("src", "a.txt", []byte("hello"), time.Now())
	driver.Put("dst", "b.txt", []byte("world"), time.Now())

	src, err := driver.ResolveSource(context.Background(), storage.Location{
		Scheme:   storage.SchemeFile,
		Path:     "src/",
		Trailing: true,
	})
	if err != nil {
		t.Fatalf("ResolveSource() = %v", err)
	}

	var keys []string
	if err := src.Endpoint.List(context.Background(), "", func(info storage.FileInfo) error {
		keys = append(keys, info.Key)
		return nil
	}); err != nil {
		t.Fatalf("List() = %v", err)
	}

	if len(keys) != 1 || keys[0] != "a.txt" {
		t.Errorf("the listing is %v, want [a.txt]", keys)
	}
}

func TestDriverListsTheInformationBelowANamespace(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	modTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	driver.Put("src", "a.txt", []byte("hello"), modTime)

	src, err := driver.ResolveSource(context.Background(), storage.Location{
		Scheme:   storage.SchemeFile,
		Path:     "src/",
		Trailing: true,
	})
	if err != nil {
		t.Fatalf("ResolveSource() = %v", err)
	}

	if err := src.Endpoint.List(context.Background(), "", func(info storage.FileInfo) error {
		if info.Key != "a.txt" {
			t.Errorf("key = %q, want %q", info.Key, "a.txt")
		}
		if info.Size != 5 {
			t.Errorf("size = %d, want 5", info.Size)
		}
		if !info.ModTime.Equal(modTime) {
			t.Errorf("modification time = %v, want %v", info.ModTime, modTime)
		}
		return nil
	}); err != nil {
		t.Fatalf("List() = %v", err)
	}
}

func TestDriverKeepsAReplacementWithTheSameLengthAndTime(t *testing.T) {
	t.Parallel()

	driver := memory.New()
	modTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	driver.Put("src", "a.txt", []byte("same"), modTime)

	src, err := driver.ResolveSource(context.Background(), storage.Location{
		Scheme: storage.SchemeFile,
		Path:   "src/",
	})
	if err != nil {
		t.Fatalf("ResolveSource() = %v", err)
	}
	var listed storage.FileInfo
	if err := src.Endpoint.List(context.Background(), "a.txt", func(info storage.FileInfo) error {
		listed = info
		return nil
	}); err != nil {
		t.Fatalf("List() = %v", err)
	}

	// A second write of the same length and time is still another version
	// of the file, because every write gets its own generation.
	driver.Put("src", "a.txt", []byte("same"), modTime)

	deleter := src.Endpoint.(storage.ConditionalDeleter)
	if err := deleter.DeleteIfUnchanged(context.Background(), "a.txt", listed); !errors.Is(err, storage.ErrChanged) {
		t.Errorf("DeleteIfUnchanged() = %v, want %v", err, storage.ErrChanged)
	}
	data, ok := driver.Get("src", "a.txt")
	if !ok || string(data) != "same" {
		t.Errorf("the replacement is %q, %v; want it to survive", data, ok)
	}
}
