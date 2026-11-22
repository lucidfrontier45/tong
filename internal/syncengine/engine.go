// Package syncengine implements the rsync-compatible transfer logic.
//
// The engine is pure logic: it knows nothing about filesystems or S3. It
// compares the listings of two endpoints and copies the files that differ.
package syncengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/lucidfrontier45/tong/internal/filter"
	"github.com/lucidfrontier45/tong/internal/storage"
)

// Exit codes, following rsync.
const (
	// CodeOK means that everything was transferred.
	CodeOK = 0
	// CodeUsage means that the command line was invalid.
	CodeUsage = 1
	// CodeSelect means that a source or destination could not be selected.
	CodeSelect = 3
	// CodePartial means that some files could not be transferred.
	CodePartial = 23
	// CodeVanished means that some source files disappeared before they
	// could be transferred.
	CodeVanished = 24
)

// Options control a sync run.
type Options struct {
	// Recursive enables recursion into subdirectories.
	Recursive bool
	// Times compares and preserves modification times.
	Times bool
	// SizeOnly compares sizes only.
	SizeOnly bool
	// Update skips files that are newer on the destination.
	Update bool
	// Existing skips files that do not exist on the destination.
	Existing bool
	// IgnoreExisting skips files that already exist on the destination.
	IgnoreExisting bool
	// Delete removes the files that the destination has but the sources
	// do not.
	Delete bool
	// DryRun prints the plan without transferring anything.
	DryRun bool
	// Verbosity is the number of -v flags.
	Verbosity int
	// MaxSize skips files that are larger than this many bytes. Zero
	// means no limit.
	MaxSize int64
	// MinSize skips files that are smaller than this many bytes.
	MinSize int64
	// Rules are the ordered include and exclude rules.
	Rules []filter.Rule
}

// item is a single file that has to be transferred.
type item struct {
	src        storage.Endpoint
	srcKey     string
	srcDisplay string
	destKey    string
	info       storage.FileInfo
}

// plan holds the state of the comparison between the sources and the
// destination.
type plan struct {
	opts    Options
	matcher *filter.Matcher
	// dest is the resolved destination.
	dest storage.Resolved
	// destFiles holds the files that the destination currently has.
	destFiles map[string]storage.FileInfo
	// planned holds the destination keys that the sources provide.
	planned map[string]bool
	// items holds the files that have to be transferred.
	items []item
}

// Run synchronises the sources into the destination and returns the exit
// code of the run.
func Run(ctx context.Context, sources []storage.Resolved, dest storage.Resolved, opts Options, stdout, stderr io.Writer) int {
	matcher, err := filter.New(opts.Rules)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tong: %v\n", err)
		return CodeUsage
	}

	if !dest.IsDir && (len(sources) != 1 || sources[0].IsDir) {
		_, _ = fmt.Fprintf(stderr, "tong: %s: cannot copy a directory onto a file\n", dest.Display)
		return CodeSelect
	}

	// The modification times of the destination only matter when they are
	// compared or preserved.
	useModTime(dest.Endpoint, opts.Times || opts.Update)

	destFiles, err := listDest(ctx, dest)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tong: cannot read %s: %v\n", dest.Display, err)
		return CodePartial
	}

	p := &plan{
		opts:      opts,
		matcher:   matcher,
		dest:      dest,
		destFiles: destFiles,
		planned:   make(map[string]bool),
	}

	for _, src := range sources {
		if err := p.addSource(ctx, src); err != nil {
			_, _ = fmt.Fprintf(stderr, "tong: cannot read %s: %v\n", src.Display, err)
			return CodePartial
		}
	}

	return p.execute(ctx, stdout, stderr)
}

// useModTime tells an endpoint whether the modification times of the files
// it lists are needed.
func useModTime(endpoint storage.Endpoint, enabled bool) {
	if aware, ok := endpoint.(storage.ModTimeAware); ok {
		aware.UseModTime(enabled)
	}
}

// addSource walks a source and collects the files that have to be copied.
func (p *plan) addSource(ctx context.Context, src storage.Resolved) error {
	// The modification times of the source only matter when they are
	// compared or preserved.
	useModTime(src.Endpoint, p.opts.Times || p.opts.Update)

	if !src.IsDir {
		info, err := lookup(ctx, src.Endpoint, src.FileKey)
		if err != nil {
			return err
		}
		p.consider(src, src.FileKey, info)
		return nil
	}

	if !p.opts.Recursive {
		if shallow, ok := src.Endpoint.(storage.ShallowLister); ok {
			return shallow.ListShallow(ctx, "", func(info storage.FileInfo) error {
				p.consider(src, info.Key, info)
				return nil
			})
		}
		// The backend cannot list a single level, so fall back to a full
		// listing and keep the top level only.
		return src.Endpoint.List(ctx, "", func(info storage.FileInfo) error {
			if strings.Contains(info.Key, "/") {
				return nil
			}
			p.consider(src, info.Key, info)
			return nil
		})
	}

	return src.Endpoint.List(ctx, "", func(info storage.FileInfo) error {
		p.consider(src, info.Key, info)
		return nil
	})
}

// consider decides whether a source file has to be copied.
func (p *plan) consider(src storage.Resolved, key string, info storage.FileInfo) {
	destKey := p.destKey(src, key)

	// The filters match the path of the file in the destination, which is
	// the transfer root that rsync uses. A source without a trailing
	// separator keeps its own name in that path.
	if !p.matcher.Match(destKey, false) {
		return
	}
	if p.opts.MinSize > 0 && info.Size < p.opts.MinSize {
		return
	}
	if p.opts.MaxSize > 0 && info.Size > p.opts.MaxSize {
		return
	}

	p.planned[destKey] = true

	dst, exists := p.destFiles[destKey]
	if !needsTransfer(info, dst, exists, p.opts) {
		return
	}

	srcDisplay := display(src.Display, key)
	if !src.IsDir {
		// The source is a single file, so it already is its own path.
		srcDisplay = src.Display
	}

	p.items = append(p.items, item{
		src:        src.Endpoint,
		srcKey:     key,
		srcDisplay: srcDisplay,
		destKey:    destKey,
		info:       info,
	})
}

// destKey maps a source key to its key in the destination.
func (p *plan) destKey(src storage.Resolved, key string) string {
	if !p.dest.IsDir {
		// The destination is an existing file, so it keeps its own name.
		return p.dest.Name
	}
	return path.Join(src.Name, key)
}

// destPath renders the path of a destination key.
func (p *plan) destPath(key string) string {
	if !p.dest.IsDir {
		// The destination is a single file, so it already is its own path.
		return p.dest.Display
	}
	return display(p.dest.Display, key)
}

// execute copies the planned files and removes the extraneous ones.
func (p *plan) execute(ctx context.Context, stdout, stderr io.Writer) int {
	failed, vanished := 0, 0

	for _, it := range p.items {
		srcPath := it.srcDisplay
		destPath := p.destPath(it.destKey)
		if p.opts.DryRun {
			_, _ = fmt.Fprintf(stdout, "would copy %s -> %s\n", srcPath, destPath)
			continue
		}
		if err := p.copyFile(ctx, it); err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, storage.ErrNotFound) {
				vanished++
			} else {
				failed++
			}
			_, _ = fmt.Fprintf(stderr, "tong: %v\n", err)
			continue
		}
		if p.opts.Verbosity > 0 {
			_, _ = fmt.Fprintln(stdout, destPath)
		}
	}

	for _, key := range p.deletions() {
		destPath := p.destPath(key)
		if p.opts.DryRun {
			_, _ = fmt.Fprintf(stdout, "would delete %s\n", destPath)
			continue
		}
		if err := p.dest.Endpoint.Delete(ctx, key); err != nil {
			failed++
			_, _ = fmt.Fprintf(stderr, "tong: %v\n", err)
			continue
		}
		if p.opts.Verbosity > 0 {
			_, _ = fmt.Fprintf(stdout, "deleting %s\n", destPath)
		}
	}

	switch {
	case failed > 0:
		return CodePartial
	case vanished > 0:
		return CodeVanished
	default:
		return CodeOK
	}
}

// deletions returns the destination keys that the sources do not provide.
func (p *plan) deletions() []string {
	if !p.opts.Delete {
		return nil
	}
	if !p.opts.Recursive {
		// Without recursion the transfer only covers the top level, so
		// nothing below it may be deleted. rsync refuses the combination
		// outright; tong only leaves the destination alone.
		return nil
	}

	var keys []string
	for key := range p.destFiles {
		if p.planned[key] {
			continue
		}
		if p.matcher.Excluded(key, false) {
			// Excluded files are protected from deletion, the way rsync
			// protects them.
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// copyFile copies a single file from a source to the destination.
func (p *plan) copyFile(ctx context.Context, it item) error {
	reader, err := it.src.Open(ctx, it.srcKey)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	modTime := time.Time{}
	if p.opts.Times {
		modTime = it.info.ModTime
	}
	return p.dest.Endpoint.Create(ctx, it.destKey, modTime, reader)
}

// listDest lists the files the destination currently holds.
func listDest(ctx context.Context, dest storage.Resolved) (map[string]storage.FileInfo, error) {
	files := make(map[string]storage.FileInfo)

	prefix := ""
	if !dest.IsDir {
		// The destination is a single file: only that file matters.
		prefix = dest.Name
	}

	err := dest.Endpoint.List(ctx, prefix, func(info storage.FileInfo) error {
		if prefix != "" && info.Key != prefix {
			return nil
		}
		files[info.Key] = info
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// lookup finds the information of a single file.
func lookup(ctx context.Context, endpoint storage.Endpoint, key string) (storage.FileInfo, error) {
	var found storage.FileInfo
	err := endpoint.List(ctx, key, func(info storage.FileInfo) error {
		if info.Key == key {
			found = info
		}
		return nil
	})
	if err != nil {
		return storage.FileInfo{}, err
	}
	if found.Key == "" {
		return storage.FileInfo{}, fmt.Errorf("%w: %s", storage.ErrNotFound, key)
	}
	return found, nil
}

// needsTransfer reports whether a source file has to be copied to the
// destination.
func needsTransfer(src, dst storage.FileInfo, exists bool, opts Options) bool {
	switch {
	case !exists:
		// The destination does not have the file yet.
		return !opts.Existing
	case opts.IgnoreExisting:
		return false
	case opts.Update && dst.ModTime.After(src.ModTime):
		// The destination has a newer copy.
		return false
	case src.Size != dst.Size:
		return true
	case opts.SizeOnly:
		return false
	case opts.Times:
		return !sameTime(src.ModTime, dst.ModTime)
	default:
		return false
	}
}

// sameTime compares two times with the one second resolution that object
// storage offers.
func sameTime(a, b time.Time) bool {
	return a.Truncate(time.Second).Equal(b.Truncate(time.Second))
}

// display joins a location and a key for output.
func display(base, key string) string {
	if base == "" {
		return key
	}
	return strings.TrimSuffix(base, "/") + "/" + key
}
