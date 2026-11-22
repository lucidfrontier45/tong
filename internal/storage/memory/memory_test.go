package memory_test

import (
	"context"
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
