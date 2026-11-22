// Package memory provides an in-memory storage driver.
//
// It exists so that the sync engine can be tested without touching a
// filesystem, which also proves that the engine only uses the storage seam.
package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lucidfrontier45/tong/internal/storage"
)

// Driver is an in-memory storage driver. The path of a location selects the
// namespace the files live in.
type Driver struct {
	mu    sync.Mutex
	files map[string]blob
}

// blob is a file held in memory.
type blob struct {
	info storage.FileInfo
	data []byte
}

// New returns an empty in-memory driver.
func New() *Driver {
	return &Driver{files: make(map[string]blob)}
}

// ResolveSource resolves a source location.
func (d *Driver) ResolveSource(_ context.Context, loc storage.Location) (storage.Resolved, error) {
	root := namespace(loc.Path)
	name := ""
	if !loc.Trailing {
		name = path.Base(root)
	}
	return storage.Resolved{
		Endpoint: &endpoint{driver: d, root: root},
		Name:     name,
		IsDir:    true,
		Display:  loc.Path,
	}, nil
}

// ResolveDest resolves a destination location.
func (d *Driver) ResolveDest(_ context.Context, loc storage.Location, singleFile bool) (storage.Resolved, error) {
	root := namespace(loc.Path)
	if singleFile && !loc.Trailing && root != "" {
		parent := path.Dir(root)
		if parent == "." {
			parent = ""
		}
		return storage.Resolved{
			Endpoint: &endpoint{driver: d, root: parent},
			Name:     path.Base(root),
			Display:  loc.Path,
		}, nil
	}
	return storage.Resolved{
		Endpoint: &endpoint{driver: d, root: root},
		IsDir:    true,
		Display:  loc.Path,
	}, nil
}

// Put stores a file below a namespace.
func (d *Driver) Put(root, key string, data []byte, modTime time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()

	full := fullKey(namespace(root), key)
	d.files[full] = blob{
		info: storage.FileInfo{Key: key, Size: int64(len(data)), ModTime: modTime},
		data: data,
	}
}

// Get returns the contents of a file below a namespace.
func (d *Driver) Get(root, key string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	b, ok := d.files[fullKey(namespace(root), key)]
	if !ok {
		return nil, false
	}
	return b.data, true
}

// Keys lists the keys of the files below a namespace, in order.
func (d *Driver) Keys(root string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	var keys []string
	for full := range d.files {
		key, ok := relative(namespace(root), full)
		if !ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// relative returns the key of a file inside a namespace.
func relative(root, full string) (string, bool) {
	if root == "" {
		return full, true
	}
	if after, ok := strings.CutPrefix(full, root+"/"); ok {
		return after, true
	}
	return "", false
}

// endpoint is a rooted view of an in-memory namespace.
type endpoint struct {
	driver *Driver
	root   string
}

// List calls fn for every file below prefix.
func (e *endpoint) List(ctx context.Context, prefix string, fn func(storage.FileInfo) error) error {
	e.driver.mu.Lock()
	files := make(map[string]blob, len(e.driver.files))
	maps.Copy(files, e.driver.files)
	e.driver.mu.Unlock()

	var keys []string
	for key := range files {
		rel, ok := relative(e.root, key)
		if !ok {
			continue
		}
		keys = append(keys, rel)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := key
		if !strings.HasPrefix(rel, prefix) {
			continue
		}
		info := files[key].info
		info.Key = rel
		if err := fn(info); err != nil {
			return err
		}
	}
	return nil
}

// ListShallow calls fn for every file directly below prefix.
func (e *endpoint) ListShallow(ctx context.Context, prefix string, fn func(storage.FileInfo) error) error {
	return e.List(ctx, prefix, func(info storage.FileInfo) error {
		if strings.Contains(info.Key, "/") {
			return nil
		}
		return fn(info)
	})
}

// Open returns the contents of a file.
func (e *endpoint) Open(_ context.Context, key string) (io.ReadCloser, error) {
	e.driver.mu.Lock()
	b, ok := e.driver.files[fullKey(e.root, key)]
	e.driver.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}
	return io.NopCloser(bytes.NewReader(b.data)), nil
}

// Create writes the contents of r to key.
func (e *endpoint) Create(_ context.Context, key string, modTime time.Time, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if modTime.IsZero() {
		modTime = time.Now()
	}

	e.driver.mu.Lock()
	defer e.driver.mu.Unlock()

	e.driver.files[fullKey(e.root, key)] = blob{
		info: storage.FileInfo{Key: key, Size: int64(len(data)), ModTime: modTime},
		data: data,
	}
	return nil
}

// Delete removes a file.
func (e *endpoint) Delete(_ context.Context, key string) error {
	e.driver.mu.Lock()
	defer e.driver.mu.Unlock()

	delete(e.driver.files, fullKey(e.root, key))
	return nil
}

// namespace turns a location path into a namespace.
func namespace(p string) string {
	return strings.Trim(strings.TrimSpace(p), "/")
}

// fullKey joins a namespace and a key.
func fullKey(root, key string) string {
	if root == "" {
		return key
	}
	return root + "/" + strings.TrimPrefix(key, "/")
}
