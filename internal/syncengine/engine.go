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
	"sync"
	"sync/atomic"
	"time"

	"github.com/lucidfrontier45/tong/internal/filter"
	"github.com/lucidfrontier45/tong/internal/ratelimit"
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
	// Parallel is the number of files that are transferred at the same
	// time. Zero or a negative value transfers one file at a time.
	Parallel int
	// BWLimit is the bandwidth budget of the run in bytes per second,
	// shared by every transfer. Zero means no limit.
	BWLimit int64
	// Stats prints a summary of the run at the end.
	Stats bool
	// RemoveSourceFiles deletes the source files that are in sync with
	// the destination, which turns the run into a one-way move.
	RemoveSourceFiles bool
}

// decision is what comparing a source file with its destination decides.
type decision int

const (
	// decisionTransfer means that the file has to be copied.
	decisionTransfer decision = iota
	// decisionInSync means that the destination already holds the file.
	decisionInSync
	// decisionExcluded means that the file is not part of the transfer
	// list, because an option such as --existing left it out.
	decisionExcluded
)

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
	// removals holds the source files that are already in sync and that
	// --remove-source-files therefore deletes without a transfer.
	removals []removal
	// limiter is the bandwidth budget shared by every transfer, or nil
	// when the run is not limited.
	limiter *ratelimit.Limiter
	// stats counts what the run did.
	stats stats
	// printMu serialises the lines that the workers write.
	printMu sync.Mutex
}

// removal is a source file that is already in sync with the destination.
type removal struct {
	src     storage.Endpoint
	key     string
	display string
}

// stats counts what a run did. The counters are updated from the workers,
// so they are atomic.
type stats struct {
	// total is the number of files in the transfer list.
	total atomic.Int64
	// transferred is the number of files that were copied.
	transferred atomic.Int64
	// skipped is the number of files that were already in sync.
	skipped atomic.Int64
	// deleted is the number of extraneous destination files that were
	// deleted.
	deleted atomic.Int64
	// removed is the number of source files that --remove-source-files
	// deleted.
	removed atomic.Int64
	// bytes is the number of bytes that were copied.
	bytes atomic.Int64
	// errors is the number of operations that failed.
	errors atomic.Int64
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
		limiter:   ratelimit.New(opts.BWLimit, nil),
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
	switch compare(info, dst, exists, p.opts) {
	case decisionExcluded:
		// The file is not part of the transfer list.
		return
	case decisionInSync:
		// The destination already holds the file. rsync removes such a
		// source too, so the file is in sync either way.
		p.stats.total.Add(1)
		p.stats.skipped.Add(1)
		if p.opts.RemoveSourceFiles {
			p.removals = append(p.removals, removal{
				src:     src.Endpoint,
				key:     key,
				display: srcDisplay(src, key),
			})
		}
		return
	case decisionTransfer:
		p.stats.total.Add(1)
	}

	srcDisplay := srcDisplay(src, key)
	p.items = append(p.items, item{
		src:        src.Endpoint,
		srcKey:     key,
		srcDisplay: srcDisplay,
		destKey:    destKey,
		info:       info,
	})
}

// srcDisplay renders the path of a source file for output.
func srcDisplay(src storage.Resolved, key string) string {
	if !src.IsDir {
		// The source is a single file, so it already is its own path.
		return src.Display
	}
	return display(src.Display, key)
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
	if p.opts.DryRun {
		p.preview(stdout)
		p.report(stdout)
		return CodeOK
	}

	failed, vanished := 0, 0
	for _, err := range p.transferAll(ctx, stdout, stderr) {
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, storage.ErrNotFound):
			vanished++
		default:
			failed++
		}
	}

	// A file that is already in sync is removed as well, because
	// --remove-source-files turns the transfer list into a move.
	failed += p.removeSynced(ctx, stderr)

	// The extraneous files of the destination are deleted after the
	// transfers, the way rsync does it.
	failed += p.deleteExtraneous(ctx, stdout, stderr)

	p.report(stdout)

	switch {
	case failed > 0:
		return CodePartial
	case vanished > 0:
		return CodeVanished
	default:
		return CodeOK
	}
}

// transferAll copies the planned files with a bounded number of workers and
// returns the error of every file, in the order of the plan.
func (p *plan) transferAll(ctx context.Context, stdout, stderr io.Writer) []error {
	errs := make([]error, len(p.items))
	if len(p.items) == 0 {
		return errs
	}

	// A fixed number of workers reads from the jobs channel, which keeps
	// at most Parallel transfers in flight. The workers write their own
	// result, so the slice needs no lock.
	jobs := make(chan int)
	workers := min(max(p.opts.Parallel, 1), len(p.items))

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range jobs {
				errs[i] = p.transfer(ctx, p.items[i], stdout, stderr)
			}
		})
	}
	for i := range p.items {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	return errs
}

// transfer copies one file, removes its source when the run asks for it and
// prints the result, which is what interleaves the output of the workers
// the way rsync interleaves its own.
func (p *plan) transfer(ctx context.Context, it item, stdout, stderr io.Writer) error {
	bytesRead, err := p.copyFile(ctx, it)
	if err != nil {
		p.stats.errors.Add(1)
		p.print(stderr, "tong: %v\n", err)
		return err
	}
	p.stats.transferred.Add(1)
	p.stats.bytes.Add(bytesRead)

	if p.opts.RemoveSourceFiles {
		if err := it.src.Delete(ctx, it.srcKey); err != nil {
			p.stats.errors.Add(1)
			p.print(stderr, "tong: %v\n", err)
			return err
		}
		p.stats.removed.Add(1)
	}

	if p.opts.Verbosity > 0 {
		p.print(stdout, "%s\n", p.destPath(it.destKey))
	}
	return nil
}

// removeSynced deletes the source files that are already in sync with the
// destination and returns the number of removals that failed.
func (p *plan) removeSynced(ctx context.Context, stderr io.Writer) int {
	failed := 0
	for _, r := range p.removals {
		if err := r.src.Delete(ctx, r.key); err != nil {
			failed++
			p.stats.errors.Add(1)
			p.print(stderr, "tong: %v\n", err)
			continue
		}
		p.stats.removed.Add(1)
	}
	return failed
}

// deleteExtraneous deletes the destination files that the sources do not
// provide and returns the number of deletions that failed.
func (p *plan) deleteExtraneous(ctx context.Context, stdout, stderr io.Writer) int {
	failed := 0
	for _, key := range p.deletions() {
		if err := p.dest.Endpoint.Delete(ctx, key); err != nil {
			failed++
			p.stats.errors.Add(1)
			p.print(stderr, "tong: %v\n", err)
			continue
		}
		p.stats.deleted.Add(1)
		if p.opts.Verbosity > 0 {
			p.print(stdout, "deleting %s\n", p.destPath(key))
		}
	}
	return failed
}

// preview prints what the run would do, without changing anything.
func (p *plan) preview(stdout io.Writer) {
	for _, it := range p.items {
		p.print(stdout, "would copy %s -> %s\n", it.srcDisplay, p.destPath(it.destKey))
	}
	for _, key := range p.deletions() {
		p.print(stdout, "would delete %s\n", p.destPath(key))
	}
	if !p.opts.RemoveSourceFiles {
		return
	}
	for _, it := range p.items {
		p.print(stdout, "would remove %s\n", it.srcDisplay)
	}
	for _, r := range p.removals {
		p.print(stdout, "would remove %s\n", r.display)
	}
}

// report prints the summary of the run. The keys are fixed and stable, so
// that they can be read with a grep or an awk.
func (p *plan) report(stdout io.Writer) {
	if !p.opts.Stats {
		return
	}
	p.print(stdout,
		"files_total=%d\n"+
			"files_transferred=%d\n"+
			"files_skipped=%d\n"+
			"files_deleted=%d\n"+
			"files_removed=%d\n"+
			"bytes_transferred=%d\n"+
			"errors=%d\n",
		p.stats.total.Load(),
		p.stats.transferred.Load(),
		p.stats.skipped.Load(),
		p.stats.deleted.Load(),
		p.stats.removed.Load(),
		p.stats.bytes.Load(),
		p.stats.errors.Load(),
	)
}

// print writes a line of output, which serialises the workers so that their
// lines do not interleave.
func (p *plan) print(w io.Writer, format string, args ...any) {
	p.printMu.Lock()
	defer p.printMu.Unlock()

	_, _ = fmt.Fprintf(w, format, args...)
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

// copyFile copies a single file from a source to the destination and
// returns the number of bytes that were read.
func (p *plan) copyFile(ctx context.Context, it item) (int64, error) {
	reader, err := it.src.Open(ctx, it.srcKey)
	if err != nil {
		return 0, err
	}
	defer func() { _ = reader.Close() }()

	// One limiter throttles every transfer, so that --bwlimit is a global
	// budget instead of one budget per file, and so that the backpressure
	// reaches the source.
	var source io.Reader = reader
	if p.limiter != nil {
		source = p.limiter.Reader(source)
	}
	counted := &countingReader{r: source}

	modTime := time.Time{}
	if p.opts.Times {
		modTime = it.info.ModTime
	}

	// A backend that can use the size of the file plans a multipart
	// transfer with it instead of buffering the stream.
	if sized, ok := p.dest.Endpoint.(storage.SizedCreator); ok {
		err = sized.CreateSized(ctx, it.destKey, modTime, it.info.Size, counted)
	} else {
		err = p.dest.Endpoint.Create(ctx, it.destKey, modTime, counted)
	}
	if err != nil {
		return counted.n, err
	}
	return counted.n, nil
}

// countingReader counts the bytes that pass through it.
type countingReader struct {
	r io.Reader
	n int64
}

// Read reads from the underlying reader and counts the bytes.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
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

// compare decides what happens to a source file: it may have to be copied,
// it may already be in sync, or an option may leave it out of the transfer
// list entirely.
func compare(src, dst storage.FileInfo, exists bool, opts Options) decision {
	switch {
	case !exists:
		// The destination does not have the file yet.
		if opts.Existing {
			return decisionExcluded
		}
		return decisionTransfer
	case opts.IgnoreExisting:
		return decisionExcluded
	case opts.Update && dst.ModTime.After(src.ModTime):
		// The destination has a newer copy.
		return decisionExcluded
	case src.Size != dst.Size:
		return decisionTransfer
	case opts.SizeOnly:
		return decisionInSync
	case opts.Times:
		if sameTime(src.ModTime, dst.ModTime) {
			return decisionInSync
		}
		return decisionTransfer
	default:
		return decisionInSync
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
