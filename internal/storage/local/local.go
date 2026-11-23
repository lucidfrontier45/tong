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

	endpoint, err := newEndpoint(loc.Path)
	if err != nil {
		return storage.Resolved{}, fmt.Errorf("cannot resolve %s: %w", loc.Path, err)
	}
	name := ""
	if !loc.Trailing {
		name = filepath.Base(filepath.Clean(loc.Path))
		if hasParentComponent(loc.Path) {
			name = filepath.Base(endpoint.root)
		}
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
	// Cleaning a path with ".." before resolving symlinks can select a
	// different directory than the OS did when it statted the input.
	if hasParentComponent(dir) {
		resolved, err := resolveParent(dir)
		if err != nil {
			return nil, err
		}
		dir = resolved
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &endpoint{root: root}, nil
}

// hasParentComponent reports whether cleaning could bypass a symlink.
func hasParentComponent(name string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(name), "/") {
		if part == ".." {
			return true
		}
	}
	return false
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

// Identity returns a token that is equal for every path that reaches the
// same file, so that a file reached through a link or under a different
// spelling of its name — Report and report on a case-insensitive
// filesystem, for example — compares equal as well.
func (e *endpoint) Identity(_ context.Context, key string) (string, error) {
	full, err := e.resolve(key)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if device, inode, ok := fileIDOf(info); ok {
		// The identity of the file itself, because two spellings of one
		// name must not produce two identities on a filesystem that folds
		// them together.
		return strconv.FormatUint(device, 10) + ":" + strconv.FormatUint(inode, 10), nil
	}
	// The platform exposes no file id, so the resolved path is the closest
	// identity there is.
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

// CheckMoveDestinations rejects moves that would need an entry to be both
// a file and a directory.
func (e *endpoint) CheckMoveDestinations(ctx context.Context, keys []string) error {
	entries := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := e.EntryIdentity(ctx, key)
		if err != nil {
			return err
		}
		// Replacing a link to a directory could invalidate another item's
		// path, even when that item's canonical path is outside this tree.
		if info, err := os.Stat(entry); err == nil {
			if info.IsDir() {
				return fmt.Errorf("%s is already a directory or a link to one", entry)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		entries[entry] = struct{}{}
	}
	for entry := range entries {
		for child := entry; ; {
			parent := filepath.Dir(child)
			if parent == child {
				break
			}
			if _, exists := entries[parent]; exists {
				return fmt.Errorf("%s is both a destination file and a parent of %s", parent, entry)
			}
			child = parent
		}
	}
	return nil
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
	// Split without cleaning: an unresolved parent can still contain
	// symlinks followed by "..". Trim trailing separators so that Lstat
	// detects a dangling link rather than looking for a directory.
	for len(dir) > 0 && os.IsPathSeparator(dir[len(dir)-1]) {
		dir = dir[:len(dir)-1]
	}
	parent, name := filepath.Split(dir)
	if name == "" || name == ".." {
		return "", err
	}
	// A dangling symlink is not a directory that Create can make.
	if _, statErr := os.Lstat(dir); statErr == nil {
		return "", err
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", statErr
	}
	if parent == "" {
		parent = "."
	}
	real, err = resolveParent(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, name), nil
}

// DeleteIfUnchanged removes the file stored under key when it still matches
// the information that was listed.
//
// The file is checked through an open handle and moved to a private name
// before it is unlinked, so that the entry which is removed is the entry
// which was checked. A replacement that a producer renames onto the key in
// the meantime is put back, and the removal fails with [storage.ErrChanged].
func (e *endpoint) DeleteIfUnchanged(ctx context.Context, key string, info storage.FileInfo) error {
	full, err := e.resolve(key)
	if err != nil {
		return err
	}
	if info.Version == "" {
		// There is nothing to compare against, so the file is removed
		// without a check.
		return e.Delete(ctx, key)
	}

	file, err := os.Open(full) //nolint:gosec // resolve confines the key to the root
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The file went away, so there is nothing left to remove.
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()

	// versionOf covers the size, the modification time and the identity of
	// the file, so a replacement of the same length and time is refused.
	current, err := file.Stat()
	if err != nil {
		return err
	}
	if versionOf(current) != info.Version {
		return fmt.Errorf("%w: %s", storage.ErrChanged, key)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Renaming the checked entry to a name that only this call knows keeps
	// a concurrent producer from renaming a replacement onto the name that
	// is about to be unlinked.
	name, err := tempName(filepath.Dir(full))
	if err != nil {
		return err
	}
	if err := os.Rename(full, name); err != nil {
		return err
	}

	moved, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("cannot verify the staged removal of %s: %w", name, err)
	}
	if os.SameFile(moved, current) {
		// The entry at the private name is the file that was checked, so
		// removing it cannot delete a replacement.
		return os.Remove(name)
	}
	// The key was replaced while the entry was staged. Put the replacement
	// back, unless yet another entry took its place, and keep the source
	// either way.
	if _, statErr := os.Lstat(full); errors.Is(statErr, fs.ErrNotExist) {
		if err := os.Rename(name, full); err != nil {
			return fmt.Errorf("cannot move %s back to %s: %w", name, full, err)
		}
	} else if statErr != nil {
		return fmt.Errorf("cannot check %s: %w", full, statErr)
	}
	return fmt.Errorf("%w: %s", storage.ErrChanged, key)
}

// fileIDOf returns the numbers the operating system uses to tell files
// apart, and reports false when the platform does not expose them.
func fileIDOf(info fs.FileInfo) (uint64, uint64, bool) {
	sys := reflect.ValueOf(info.Sys())
	if !sys.IsValid() || sys.Kind() != reflect.Pointer || sys.IsNil() {
		return 0, 0, false
	}
	sys = sys.Elem()
	device, inode := sys.FieldByName("Dev"), sys.FieldByName("Ino")
	if device.IsValid() && inode.IsValid() && device.CanUint() && inode.CanUint() {
		return device.Uint(), inode.Uint(), true
	}
	// Windows exposes a volume serial number and a file index instead,
	// which is what os.SameFile compares there.
	volume, idxhi, idxlo := sys.FieldByName("vol"), sys.FieldByName("idxhi"), sys.FieldByName("idxlo")
	if volume.IsValid() && idxhi.IsValid() && idxlo.IsValid() &&
		volume.CanUint() && idxhi.CanUint() && idxlo.CanUint() {
		return volume.Uint(), idxhi.Uint()<<32 | idxlo.Uint(), true
	}
	return 0, 0, false
}

// versionOf returns a token that changes when the contents or the
// modification time of a file change. It is the check that rsync applies
// before it removes a source, with the sub-second resolution that the
// filesystem offers and the identity of the file.
func versionOf(info fs.FileInfo) string {
	version := strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
	if device, inode, ok := fileIDOf(info); ok {
		return version + ":" + strconv.FormatUint(device, 10) + ":" + strconv.FormatUint(inode, 10)
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
