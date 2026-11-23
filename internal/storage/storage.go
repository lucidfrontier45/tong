// Package storage defines the driver seam that tong's sync engine uses to
// talk to storage backends.
//
// The sync engine never imports a concrete driver: it only knows about
// [Endpoint] and about the optional capability interfaces declared here.
// Adding a backend (Google Cloud Storage, Azure Blob Storage, ...) means
// implementing [Driver] and registering it with a [Registry].
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Scheme identifies a storage backend.
type Scheme string

const (
	// SchemeFile is the local filesystem.
	SchemeFile Scheme = "file"
	// SchemeS3 is Amazon S3 and S3-compatible services.
	SchemeS3 Scheme = "s3"
)

// Errors returned while parsing or resolving locations.
var (
	// ErrUnsupportedScheme is returned for URL schemes tong cannot handle.
	ErrUnsupportedScheme = errors.New("unsupported storage scheme")
	// ErrNotFound is returned when a location does not exist.
	ErrNotFound = errors.New("location not found")
	// ErrChanged is returned by [ConditionalDeleter] when a file no longer
	// matches the information the caller listed.
	ErrChanged = errors.New("file changed since it was listed")
)

// FileInfo describes a single file or object.
type FileInfo struct {
	// Key is the path of the file relative to the endpoint root. Keys are
	// always slash-separated, even on Windows.
	Key string
	// Size is the size of the file in bytes.
	Size int64
	// ModTime is the last modification time of the file.
	ModTime time.Time
	// Checksum is an optional provider checksum, such as an ETag.
	Checksum string
	// Version is an opaque token that identifies the revision of the file
	// that was listed, such as its size and modification time or its ETag.
	// The engine uses it to notice a file that changed after it was
	// listed.
	Version string
}

// Endpoint is a rooted view of a storage location.
//
// Keys are relative, slash-separated paths: the key "a/b.txt" of an endpoint
// rooted at "/srv/data" refers to the file "/srv/data/a/b.txt".
type Endpoint interface {
	// List calls fn for every file below prefix. Implementations must
	// stream their results: a listing is never materialised in memory.
	List(ctx context.Context, prefix string, fn func(FileInfo) error) error
	// Open returns a reader for key. The caller closes the reader.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Create writes the contents of r to key, replacing it if it exists.
	// A zero modTime means "now".
	Create(ctx context.Context, key string, modTime time.Time, r io.Reader) error
	// Delete removes key.
	Delete(ctx context.Context, key string) error
}

// Copier is an optional capability: the backend can copy a file without
// downloading it. tong checks for the capability instead of assuming a
// scheme, so that any backend may implement it.
type Copier interface {
	// Copy copies srcKey to dstKey inside the endpoint.
	Copy(ctx context.Context, srcKey, dstKey string) error
}

// Mover is an optional capability: the backend can move a file without
// downloading it.
type Mover interface {
	// Move moves srcKey to dstKey inside the endpoint.
	Move(ctx context.Context, srcKey, dstKey string) error
}

// ShallowLister is an optional capability: the backend can list only the
// immediate children of a prefix, which is what a non-recursive sync needs.
type ShallowLister interface {
	// ListShallow calls fn for every file directly below prefix.
	ListShallow(ctx context.Context, prefix string, fn func(FileInfo) error) error
}

// SizedCreator is an optional capability: the backend learns how large the
// file it is asked to write is.
//
// A backend that splits an upload into parts uses the size to decide whether
// the file fits into a single request and how large the parts have to be,
// instead of buffering the stream to find out.
type SizedCreator interface {
	// CreateSized is [Endpoint.Create] with the size of the file in bytes.
	// The size comes from the listing of the source, so a file that changed
	// in between still transfers the bytes it holds, but the backend may
	// rely on the size and report a mismatch as an error.
	CreateSized(ctx context.Context, key string, modTime time.Time, size int64, r io.Reader) error
}

// ModTimeAware is an optional capability: the backend reports the
// modification time of the files it lists, and lets the caller say whether
// those times are needed.
//
// Some backends need an extra request per file to learn the modification
// time of a file. They skip that request when the caller does not compare
// times.
type ModTimeAware interface {
	// UseModTime tells the endpoint whether the modification times of the
	// listed files matter.
	UseModTime(enabled bool)
}

// Identifier is an optional capability: the backend can report a stable
// identity for a key, so that the engine can tell whether a source and a
// destination refer to the same file.
//
// Two keys that resolve to the same file return the same value. A local
// backend returns the resolved absolute path, which also catches a file that
// is reached through a symlink or a relative component; an object store
// returns the bucket and the key.
type Identifier interface {
	// Identity returns the identity of the file stored under key.
	Identity(ctx context.Context, key string) (string, error)
}

// EntryIdentifier identifies the entry that Create replaces and Delete
// removes. Unlike [Identifier], it does not follow a link at the final name.
type EntryIdentifier interface {
	// EntryIdentity returns the identity of the entry, even if not yet created.
	EntryIdentity(ctx context.Context, key string) (string, error)
}

// ConditionalDeleter is an optional capability: the backend deletes a key
// only while it still matches the information the caller listed, so that a
// file that was replaced in between is not deleted.
//
// A delete that finds the file changed returns [ErrChanged] and leaves the
// file in place.
type ConditionalDeleter interface {
	// DeleteIfUnchanged deletes key if it still matches info.
	DeleteIfUnchanged(ctx context.Context, key string, info FileInfo) error
}

// Location is a parsed command-line location.
type Location struct {
	// Scheme is the backend the location refers to.
	Scheme Scheme
	// Bucket is the bucket or container name. It is only used by backends
	// that have buckets, such as S3.
	Bucket string
	// Key is the object key or key prefix. It is only used by backends that
	// have keys, such as S3.
	Key string
	// Path is the filesystem path. It is only used by the local backend.
	Path string
	// Trailing reports whether the location was written with a trailing
	// path separator, which selects rsync's "copy the contents" semantics.
	Trailing bool
}

// ParseLocation parses a command-line location such as "./dist/",
// "file:///srv/dist" or "s3://bucket/prefix".
func ParseLocation(raw string) (Location, error) {
	if raw == "" {
		return Location{}, fmt.Errorf("%w: empty location", ErrUnsupportedScheme)
	}

	loc := Location{Trailing: strings.HasSuffix(raw, "/")}

	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		loc.Scheme = SchemeFile
		loc.Path = raw
		return loc, nil
	}

	switch Scheme(strings.ToLower(scheme)) {
	case SchemeFile:
		if rest == "" {
			return Location{}, fmt.Errorf("%w: %q has no path", ErrUnsupportedScheme, raw)
		}
		loc.Scheme = SchemeFile
		loc.Path = rest
		return loc, nil
	case SchemeS3:
		bucket, key, _ := strings.Cut(rest, "/")
		if bucket == "" {
			return Location{}, fmt.Errorf("%w: %q has no bucket", ErrUnsupportedScheme, raw)
		}
		loc.Scheme = SchemeS3
		loc.Bucket = bucket
		loc.Key = key
		return loc, nil
	default:
		return Location{}, fmt.Errorf("%w: %q", ErrUnsupportedScheme, scheme)
	}
}
