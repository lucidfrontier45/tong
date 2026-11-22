// Package cli wires the command line to the sync engine.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/alecthomas/kong"

	"github.com/lucidfrontier45/tong/internal/storage"
	"github.com/lucidfrontier45/tong/internal/storage/local"
	"github.com/lucidfrontier45/tong/internal/storage/s3"
	"github.com/lucidfrontier45/tong/internal/syncengine"
)

// options is the command line grammar of tong.
type options struct {
	Recursive      bool     `kong:"short='r',help='Recurse into directories'"`
	Archive        bool     `kong:"short='a',help='Archive mode: -r -t'"`
	Verbose        int      `kong:"short='v',type='counter',help='Increase verbosity (repeatable)'"`
	DryRun         bool     `kong:"short='n',name='dry-run',help='Show what would be transferred without changing anything'"`
	Delete         bool     `kong:"help='Delete extraneous files from the destination'"`
	Times          bool     `kong:"short='t',help='Preserve modification times'"`
	Compress       bool     `kong:"short='z',help='Compress file data during transfer'"`
	Checksum       bool     `kong:"short='c',help='Skip based on checksums, not size + mtime'"`
	SizeOnly       bool     `kong:"name='size-only',help='Skip based on size only'"`
	Update         bool     `kong:"short='u',help='Skip files that are newer on the destination'"`
	Existing       bool     `kong:"help='Skip creating new files on the destination'"`
	IgnoreExisting bool     `kong:"name='ignore-existing',help='Skip files that already exist on the destination'"`
	Include        []string `kong:"help='Do not exclude files matching PATTERN',sep='none',placeholder='PATTERN'"`
	Exclude        []string `kong:"help='Exclude files matching PATTERN',sep='none',placeholder='PATTERN'"`
	ExcludeFrom    []string `kong:"name='exclude-from',help='Read exclude patterns from FILE',sep='none',placeholder='FILE'"`
	MaxSize        string   `kong:"name='max-size',help='Do not transfer files larger than SIZE',placeholder='SIZE'"`
	MinSize        string   `kong:"name='min-size',help='Do not transfer files smaller than SIZE',placeholder='SIZE'"`
	BWLimit        string   `kong:"name='bwlimit',help='Limit I/O bandwidth, KiB/s',placeholder='KBPS'"`
	Partial        bool     `kong:"short='P',help='Same as --partial --progress'"`
	Stats          bool     `kong:"help='Print a transfer summary'"`
	RemoveSource   bool     `kong:"name='remove-source-files',help='Delete source files after a successful transfer'"`
	S3Endpoint     string   `kong:"name='s3-endpoint',help='Use a custom S3-compatible endpoint'"`
	S3Region       string   `kong:"name='s3-region',help='Override the AWS region'"`
	Srcs           []string `kong:"arg='true',name='src',optional='true',help='Source and destination paths'"`
}

// Run parses the command line, runs the sync and returns the exit code of
// the process.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var opts options
	exited := false
	exitCode := 0

	parser, err := kong.New(&opts,
		kong.Name("tong"),
		kong.Description("Synchronize files between local directories and Amazon S3."),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) {
			exited = true
			exitCode = code
		}),
	)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tong: %v\n", err)
		return syncengine.CodeUsage
	}

	if _, err := parser.Parse(args); err != nil {
		_, _ = fmt.Fprintf(stderr, "tong: %v\n", err)
		_, _ = fmt.Fprintln(stderr, "Run 'tong --help' for more information.")
		return syncengine.CodeUsage
	}
	if exited {
		// The help was printed.
		return exitCode
	}

	if unsupported := opts.unsupported(); len(unsupported) > 0 {
		_, _ = fmt.Fprintf(stderr, "tong: %s: not supported yet\n", strings.Join(unsupported, ", "))
		return syncengine.CodeUsage
	}

	if len(opts.Srcs) < 2 {
		_, _ = fmt.Fprintln(stderr, "tong: a source and a destination are required")
		_, _ = fmt.Fprintln(stderr, "Run 'tong --help' for more information.")
		return syncengine.CodeUsage
	}

	recursive := opts.Recursive || opts.Archive
	if opts.Delete && !recursive {
		// rsync refuses the combination, because a sync that does not
		// recurse cannot know what the destination should hold.
		_, _ = fmt.Fprintln(stderr, "tong: --delete does not work without --recursive (-r) or --archive (-a)")
		return syncengine.CodeUsage
	}

	rules, err := filterRules(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tong: %v\n", err)
		return syncengine.CodeUsage
	}

	maxSize, err := parseSize(opts.MaxSize)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tong: --max-size: %v\n", err)
		return syncengine.CodeUsage
	}
	minSize, err := parseSize(opts.MinSize)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tong: --min-size: %v\n", err)
		return syncengine.CodeUsage
	}

	registry := storage.NewRegistry()
	registry.Register(storage.SchemeFile, local.New())
	registry.Register(storage.SchemeS3, s3.New(s3.Options{
		Region:   opts.S3Region,
		Endpoint: opts.S3Endpoint,
	}))

	sources := make([]storage.Resolved, 0, len(opts.Srcs)-1)
	for _, raw := range opts.Srcs[:len(opts.Srcs)-1] {
		resolved, err := registry.ResolveSource(ctx, raw)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "tong: %v\n", err)
			return syncengine.CodeSelect
		}
		sources = append(sources, resolved)
	}

	// A single source file is copied onto the destination itself, the
	// way rsync treats it.
	singleFile := len(sources) == 1 && !sources[0].IsDir

	dest, err := registry.ResolveDest(ctx, opts.Srcs[len(opts.Srcs)-1], singleFile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tong: %v\n", err)
		return syncengine.CodeSelect
	}

	return syncengine.Run(ctx, sources, dest, syncengine.Options{
		Recursive:      recursive,
		Times:          opts.Times || opts.Archive,
		SizeOnly:       opts.SizeOnly,
		Update:         opts.Update,
		Existing:       opts.Existing,
		IgnoreExisting: opts.IgnoreExisting,
		Delete:         opts.Delete,
		DryRun:         opts.DryRun,
		Verbosity:      opts.Verbose,
		MaxSize:        maxSize,
		MinSize:        minSize,
		Rules:          rules,
	}, stdout, stderr)
}

// unsupported lists the options that tong parses but cannot honour yet.
func (o *options) unsupported() []string {
	var out []string
	if o.Compress {
		out = append(out, "-z")
	}
	if o.Checksum {
		out = append(out, "-c")
	}
	if o.Partial {
		out = append(out, "-P")
	}
	if o.Stats {
		out = append(out, "--stats")
	}
	if o.BWLimit != "" {
		out = append(out, "--bwlimit")
	}
	if o.RemoveSource {
		out = append(out, "--remove-source-files")
	}
	return out
}

// sizePattern matches the number of a size, with an optional fraction.
var sizePattern = regexp.MustCompile(`^\d+(\.\d+)?$`)

// parseSize parses a size such as "1024", "10K", "1.5M" or "2G".
func parseSize(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}

	multiplier := int64(1)
	digits := value
	switch last := digits[len(digits)-1]; last {
	case 'K', 'k':
		multiplier = 1 << 10
		digits = digits[:len(digits)-1]
	case 'M', 'm':
		multiplier = 1 << 20
		digits = digits[:len(digits)-1]
	case 'G', 'g':
		multiplier = 1 << 30
		digits = digits[:len(digits)-1]
	}

	if !sizePattern.MatchString(digits) {
		return 0, fmt.Errorf("invalid size %q", value)
	}

	// The number is parsed as an integer, so that a value that does not
	// fit into an int64 is a usage error instead of a limit that quietly
	// disappears.
	whole, fraction, _ := strings.Cut(digits, ".")
	number, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, fmt.Errorf("size %q is too large", value)
		}
		return 0, fmt.Errorf("invalid size %q", value)
	}

	// The scaled number has to leave room for the fraction.
	if number > (math.MaxInt64-(multiplier-1))/multiplier {
		return 0, fmt.Errorf("size %q is too large", value)
	}
	size := number * multiplier

	if fraction != "" {
		part, err := strconv.ParseFloat("0."+fraction, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid size %q", value)
		}
		extra := int64(math.Round(part * float64(multiplier)))
		if size > math.MaxInt64-extra {
			return 0, fmt.Errorf("size %q is too large", value)
		}
		size += extra
	}

	return size, nil
}
