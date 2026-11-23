// Package local implements the local filesystem driver.
package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
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
		endpoint, err := newEndpoint(dir)
		if err != nil {
			return storage.Resolved{}, fmt.Errorf("cannot resolve %s: %w", loc.Path, err)
		}
		return storage.Resolved{
			Endpoint: endpoint,
			FileKey:  file,
			Display:  loc.Path,
		}, nil
	}

	root := filepath.Clean(loc.Path)
	name := ""
	if !loc.Trailing {
		name = filepath.Base(root)
	}
	endpoint, err := newEndpoint(root)
	if err != nil {
		return storage.Resolved{}, fmt.Errorf("cannot resolve %s: %w", loc.Path, err)
	}
	return storage.Resolved{
		Endpoint: endpoint,
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
		return dirDest(loc)
	case err == nil:
		// The destination already exists as a file. Only a single source
		// file may be copied onto it.
		return fileDest(loc)
	case errors.Is(err, fs.ErrNotExist):
		if singleFile {
			// The destination holds the file that is copied.
			return fileDest(loc)
		}
		return dirDest(loc)
	default:
		return storage.Resolved{}, fmt.Errorf("cannot read %s: %w", loc.Path, err)
	}
}

// dirDest resolves a destination that is a directory.
func dirDest(loc storage.Location) (storage.Resolved, error) {
	endpoint, err := newEndpoint(loc.Path)
	if err != nil {
		return storage.Resolved{}, fmt.Errorf("cannot resolve %s: %w", loc.Path, err)
	}
	return storage.Resolved{
		Endpoint: endpoint,
		IsDir:    true,
		Display:  loc.Path,
	}, nil
}

// fileDest resolves a destination that holds a single file.
func fileDest(loc storage.Location) (storage.Resolved, error) {
	dir, file := filepath.Split(loc.Path)
	endpoint, err := newEndpoint(dir)
	if err != nil {
		return storage.Resolved{}, fmt.Errorf("cannot resolve %s: %w", loc.Path, err)
	}
	return storage.Resolved{
		Endpoint: endpoint,
		Name:     file,
		Display:  loc.Path,
	}, nil
}

// newEndpoint returns an endpoint rooted at a directory.
//
// The root is held as an absolute path, so that a relative location resolves
// the same way from any working directory and so that two endpoints can be
// compared by path.
func newEndpoint(dir string) (*endpoint, error) {
	root, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return nil, err
	}
	return &endpoint{root: root}, nil
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
			Version: versionOf(info),
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
			Version: versionOf(info),
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
//
// The data is written to a temporary file next to the destination and moved
// into place afterwards, the way rsync does it. A transfer that fails halfway
// therefore leaves the previous copy of the file alone.
func (e *endpoint) Create(ctx context.Context, key string, modTime time.Time, r io.Reader) error {
	full, err := e.resolve(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, dirPerm); err != nil { //nolint:gosec // directories are meant to be readable
		return err
	}

	name, err := tempName(dir)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(name) }()

	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666) //nolint:gosec // the name is random and inside the root
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, &contextReader{ctx: ctx, r: r}); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if !modTime.IsZero() {
		if err := os.Chtimes(name, modTime, modTime); err != nil {
			return err
		}
	}
	return os.Rename(name, full)
}

// tempName returns a unique name for a temporary file in a directory.
func tempName(dir string) (string, error) {
	for range 10 {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		name := filepath.Join(dir, ".tong-"+hex.EncodeToString(buf[:]))
		if _, err := os.Stat(name); errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot create a temporary file in %s", dir)
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

// Identity returns the absolute path of the file, with every symlink
// resolved, so that paths that reach the same file compare equal.
func (e *endpoint) Identity(_ context.Context, key string) (string, error) {
	full, err := e.resolve(key)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(full)
}

// EntryIdentity resolves parent directories, but keeps the final name
// because Create replaces that entry instead of following a final symlink.
func (e *endpoint) EntryIdentity(_ context.Context, key string) (string, error) {
	full, err := e.resolve(key)
	if err != nil {
		return "", err
	}
	parent, err := resolveParent(filepath.Dir(full))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(full)), nil
}

// resolveParent resolves the longest existing prefix before appending any
// directories that Create will make.
func resolveParent(dir string) (string, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	// A dangling symlink is not a directory that Create can make.
	if _, statErr := os.Lstat(dir); statErr == nil {
		return "", err
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", statErr
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		return "", err
	}
	real, err = resolveParent(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, filepath.Base(dir)), nil
}

// DeleteIfUnchanged removes the file stored under key when it still matches
// the information that was listed.
func (e *endpoint) DeleteIfUnchanged(ctx context.Context, key string, info storage.FileInfo) error {
	full, err := e.resolve(key)
	if err != nil {
		return err
	}

	current, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The file went away, so there is nothing left to remove.
			return nil
		}
		return err
	}
	if info.Version != "" && versionOf(current) != info.Version {
		return fmt.Errorf("%w: %s", storage.ErrChanged, key)
	}
	return e.Delete(ctx, key)
}

// versionOf returns a token that changes when the contents or the
// modification time of a file change. It is the check that rsync applies
// before it removes a source, with the sub-second resolution that the
// filesystem offers.
func versionOf(info fs.FileInfo) string {
	version := strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
	sys := reflect.ValueOf(info.Sys())
	if sys.IsValid() && sys.Kind() == reflect.Pointer && !sys.IsNil() {
		sys = sys.Elem()
		device, inode := sys.FieldByName("Dev"), sys.FieldByName("Ino")
		if device.IsValid() && inode.IsValid() && device.CanUint() && inode.CanUint() {
			return version + ":" + strconv.FormatUint(device.Uint(), 10) + ":" + strconv.FormatUint(inode.Uint(), 10)
		}
	}
	return version
}

// contextReader stops a copy when ctx is done, so that a cancelled run does
// not keep writing a file it will not finish.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

// Read reads from the underlying reader unless the context is done.
func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
