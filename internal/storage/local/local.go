// Package local implements the local filesystem driver.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lucidfrontier45/tong/internal/storage"
)

// dirPerm is the permission of the directories tong creates.
const dirPerm = 0o755

// Driver implements [storage.Driver] for local paths.
type Driver struct{}

// New returns a driver for the local filesystem.
func New() *Driver {
	return &Driver{}
}

// ResolveSource resolves a local path given on the command line.
func (d *Driver) ResolveSource(_ context.Context, loc storage.Location) (storage.Resolved, error) {
	info, err := os.Stat(loc.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return storage.Resolved{}, fmt.Errorf("%w: %s", storage.ErrNotFound, loc.Path)
		}
		return storage.Resolved{}, fmt.Errorf("cannot read %s: %w", loc.Path, err)
	}

	if !info.IsDir() {
		if loc.Trailing {
			return storage.Resolved{}, fmt.Errorf("%s: not a directory", loc.Path)
		}
		dir, file := filepath.Split(loc.Path)
		return storage.Resolved{
			Endpoint: &endpoint{root: filepath.Clean(dir)},
			FileKey:  file,
			Display:  loc.Path,
		}, nil
	}

	root := filepath.Clean(loc.Path)
	name := ""
	if !loc.Trailing {
		name = filepath.Base(root)
	}
	return storage.Resolved{
		Endpoint: &endpoint{root: root},
		Name:     name,
		IsDir:    true,
		Display:  loc.Path,
	}, nil
}

// ResolveDest resolves a local destination given on the command line.
func (d *Driver) ResolveDest(_ context.Context, loc storage.Location, singleFile bool) (storage.Resolved, error) {
	info, err := os.Stat(loc.Path)
	switch {
	case err == nil && info.IsDir():
		return storage.Resolved{
			Endpoint: &endpoint{root: filepath.Clean(loc.Path)},
			IsDir:    true,
			Display:  loc.Path,
		}, nil
	case err == nil:
		// The destination already exists as a file. Only a single source
		// file may be copied onto it.
		return fileDest(loc), nil
	case errors.Is(err, fs.ErrNotExist):
		if singleFile {
			// The destination holds the file that is copied.
			return fileDest(loc), nil
		}
		return storage.Resolved{
			Endpoint: &endpoint{root: filepath.Clean(loc.Path)},
			IsDir:    true,
			Display:  loc.Path,
		}, nil
	default:
		return storage.Resolved{}, fmt.Errorf("cannot read %s: %w", loc.Path, err)
	}
}

// fileDest resolves a destination that holds a single file.
func fileDest(loc storage.Location) storage.Resolved {
	dir, file := filepath.Split(loc.Path)
	return storage.Resolved{
		Endpoint: &endpoint{root: filepath.Clean(dir)},
		Name:     file,
		Display:  loc.Path,
	}
}

// endpoint is a rooted view of a local directory.
type endpoint struct {
	root string
}

// resolve maps a key to a path inside the endpoint root.
func (e *endpoint) resolve(key string) (string, error) {
	full := filepath.Join(e.root, filepath.FromSlash(key))
	if full != e.root && !strings.HasPrefix(full, e.root+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid key %q", key)
	}
	return full, nil
}

// List calls fn for every file below prefix.
func (e *endpoint) List(ctx context.Context, prefix string, fn func(storage.FileInfo) error) error {
	start, err := e.resolve(prefix)
	if err != nil {
		return err
	}

	if _, err := os.Stat(start); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The directory does not exist yet, so it holds no files.
			return nil
		}
		return err
	}

	return filepath.WalkDir(start, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			// Symbolic links and special files have no meaning in
			// object storage, so they are left alone.
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		key, err := e.key(p)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		return fn(storage.FileInfo{
			Key:     key,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	})
}

// ListShallow calls fn for every file directly below prefix.
func (e *endpoint) ListShallow(ctx context.Context, prefix string, fn func(storage.FileInfo) error) error {
	dir, err := e.resolve(prefix)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		key := path.Join(prefix, entry.Name())
		if err := fn(storage.FileInfo{
			Key:     key,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// key maps a filesystem path to its key inside the endpoint.
func (e *endpoint) key(p string) (string, error) {
	rel, err := filepath.Rel(e.root, p)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// Open opens the file stored under key.
func (e *endpoint) Open(_ context.Context, key string) (io.ReadCloser, error) {
	full, err := e.resolve(key)
	if err != nil {
		return nil, err
	}
	// The path is confined to the endpoint root by resolve.
	return os.Open(full) //nolint:gosec // path is validated above
}

// Create writes the contents of r to key.
func (e *endpoint) Create(_ context.Context, key string, modTime time.Time, r io.Reader) error {
	full, err := e.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), dirPerm); err != nil { //nolint:gosec // directories are meant to be readable
		return err
	}

	file, err := os.Create(full) //nolint:gosec // path is validated above
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, r); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if !modTime.IsZero() {
		if err := os.Chtimes(full, modTime, modTime); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes the file stored under key.
func (e *endpoint) Delete(_ context.Context, key string) error {
	full, err := e.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
