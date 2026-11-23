package cli

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/lucidfrontier45/tong/internal/filter"
	"github.com/lucidfrontier45/tong/internal/syncengine"
)

func TestRunHelp(t *testing.T) {
	t.Parallel()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Run(context.Background(), []string{"--help"}, stdout, stderr); code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "--s3-endpoint") {
		t.Errorf("the help does not mention --s3-endpoint: %s", stdout)
	}
}

func TestRunRejectsUnsupportedOptions(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"-z", "src", "dst"},
		{"-c", "src", "dst"},
		{"-P", "src", "dst"},
	} {
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		code := Run(context.Background(), args, stdout, stderr)
		if code != syncengine.CodeUsage {
			t.Errorf("Run(%v) = %d, want %d", args, code, syncengine.CodeUsage)
		}
	}
}

func TestRunRejectsAnInvalidParallelism(t *testing.T) {
	t.Parallel()

	for _, parallel := range []string{"0", "-1", "many"} {
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		code := Run(context.Background(), []string{"--parallel", parallel, "src", "dst"}, stdout, stderr)
		if code != syncengine.CodeUsage {
			t.Errorf("Run(--parallel %s) = %d, want %d", parallel, code, syncengine.CodeUsage)
		}
	}
}

func TestRunRefusesSourceRemovalOnUnverifiedS3Endpoint(t *testing.T) {
	for _, setup := range []struct {
		name string
		env  string
	}{
		{name: "flag"},
		{name: "global environment", env: "AWS_ENDPOINT_URL"},
		{name: "service environment", env: "AWS_ENDPOINT_URL_S3"},
	} {
		t.Run(setup.name, func(t *testing.T) {
			const endpoint = "http://127.0.0.1:1"
			args := []string{"--s3-region", "us-east-1", "--remove-source-files", "-r"}
			if setup.env == "" {
				args = append(args, "--s3-endpoint", endpoint)
			} else {
				t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "false")
				t.Setenv(setup.env, endpoint)
			}
			args = append(args, "s3://bucket/", t.TempDir()+"/")
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			if code := Run(t.Context(), args, stdout, stderr); code != syncengine.CodeUsage ||
				!strings.Contains(stderr.String(), "requires --s3-trust-conditional-delete") {
				t.Fatalf("Run() = %d, stderr = %q; want preflight refusal", code, stderr.String())
			}
		})
	}
}

func TestRunDefaultsToFourTransfersAtATime(t *testing.T) {
	t.Parallel()

	var opts options
	parser, err := kong.New(&opts)
	if err != nil {
		t.Fatalf("kong.New() = %v", err)
	}
	if _, err := parser.Parse([]string{"src", "dst"}); err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if opts.Parallel != 4 {
		t.Errorf("--parallel defaults to %d, want 4", opts.Parallel)
	}
}

func TestRunAppliesTheNewOptions(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("cannot write the source: %v", err)
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(context.Background(), []string{
		"-a", "--parallel", "2", "--bwlimit", "1M", "--stats", "--remove-source-files",
		src + "/", dst,
	}, stdout, stderr)
	if code != syncengine.CodeOK {
		t.Fatalf("Run() = %d, want %d (%s)", code, syncengine.CodeOK, stderr)
	}

	if got := stdout.String(); !strings.Contains(got, "files_transferred=1\n") {
		t.Errorf("the summary is %q, want it to count the transfer", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.txt")); err != nil {
		t.Errorf("a.txt was not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "a.txt")); err == nil {
		t.Error("the source file was not removed")
	}
}

func TestParseRate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  int64
	}{
		{value: "", want: 0},
		{value: "0", want: 0},
		// A bare number is KiB per second.
		{value: "100", want: 100 * 1024},
		{value: "1.5", want: 1536},
		// A single letter or the "iB" form scales by 1024.
		{value: "1K", want: 1 << 10},
		{value: "1k", want: 1 << 10},
		{value: "1KiB", want: 1 << 10},
		{value: "1kib", want: 1 << 10},
		{value: "1.5K", want: 1536},
		{value: "2M", want: 2 << 20},
		{value: "1G", want: 1 << 30},
		{value: "1T", want: 1 << 40},
		// A two-letter form ending in B scales by 1000.
		{value: "1KB", want: 1000},
		{value: "1MB", want: 1000 * 1000},
		// A rate is exact, so it may be smaller than one KiB.
		{value: "1B", want: 1},
		{value: "0.5", want: 512},
	}
	for _, tc := range tests {
		got, err := parseRate(tc.value)
		if err != nil {
			t.Errorf("parseRate(%q) = %v", tc.value, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseRate(%q) = %d, want %d", tc.value, got, tc.want)
		}
	}

	// A rate that is asked for but rounds away would mean "no limit", and a
	// malformed or overflowing rate is refused.
	for _, value := range []string{"big", "-1", "1E", "1x", "1.5.5", "0.0001", "9223372036854775807"} {
		if _, err := parseRate(value); err == nil {
			t.Errorf("parseRate(%q) = nil, want an error", value)
		}
	}
}

func TestRunNeedsASourceAndADestination(t *testing.T) {
	t.Parallel()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Run(context.Background(), []string{"src"}, stdout, stderr); code != syncengine.CodeUsage {
		t.Fatalf("Run() = %d, want %d", code, syncengine.CodeUsage)
	}
}

func TestRunRejectsUnknownSchemes(t *testing.T) {
	t.Parallel()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(context.Background(), []string{"gs://bucket/key", "dst"}, stdout, stderr)
	if code != syncengine.CodeSelect {
		t.Fatalf("Run() = %d, want %d", code, syncengine.CodeSelect)
	}
	if !strings.Contains(stderr.String(), "unsupported storage scheme") {
		t.Errorf("the error is %q, want it to mention the scheme", stderr)
	}
}

func TestRunReportsMissingSources(t *testing.T) {
	t.Parallel()

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(context.Background(), []string{filepath.Join(t.TempDir(), "gone"), "dst"}, stdout, stderr)
	if code != syncengine.CodeSelect {
		t.Fatalf("Run() = %d, want %d", code, syncengine.CodeSelect)
	}
}

func TestRunSyncsLocalDirectories(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("cannot write the source: %v", err)
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(context.Background(), []string{"-a", src + "/", dst}, stdout, stderr)
	if code != syncengine.CodeOK {
		t.Fatalf("Run() = %d, want %d (%s)", code, syncengine.CodeOK, stderr)
	}

	got, err := os.ReadFile(filepath.Join(dst, "a.txt")) //nolint:gosec // the test wrote the file
	if err != nil {
		t.Fatalf("cannot read the copy: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("a.txt = %q, want %q", got, "hello")
	}
}

func TestRunDryRunPrintsThePlan(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("cannot write the source: %v", err)
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(context.Background(), []string{"-an", src + "/", dst}, stdout, stderr)
	if code != syncengine.CodeOK {
		t.Fatalf("Run() = %d, want %d (%s)", code, syncengine.CodeOK, stderr)
	}
	if !strings.Contains(stdout.String(), "would copy") {
		t.Errorf("the plan is %q, want it to mention the copy", stdout)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.txt")); err == nil {
		t.Error("a.txt was copied, want the run to be a dry run")
	}
}

func TestRunRefusesDeleteWithoutRecursion(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("cannot write the source: %v", err)
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(context.Background(), []string{"--delete", src + "/", dst}, stdout, stderr)
	if code != syncengine.CodeUsage {
		t.Fatalf("Run() = %d, want %d", code, syncengine.CodeUsage)
	}
	if !strings.Contains(stderr.String(), "--delete") {
		t.Errorf("the error is %q, want it to mention --delete", stderr)
	}
}

func TestRunRejectsInvalidSizes(t *testing.T) {
	t.Parallel()

	for _, size := range []string{"NaN", "Inf", "-1", "1e3", "100000000000G", "9223372036854775808"} {
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		code := Run(context.Background(), []string{"--max-size", size, "src", "dst"}, stdout, stderr)
		if code != syncengine.CodeUsage {
			t.Errorf("Run(--max-size %s) = %d, want %d", size, code, syncengine.CodeUsage)
		}
	}
}

func TestRunCopiesASingleFileOntoTheDestination(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("cannot write the source: %v", err)
	}

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(context.Background(), []string{filepath.Join(src, "a.txt"), filepath.Join(dst, "b.txt")}, stdout, stderr)
	if code != syncengine.CodeOK {
		t.Fatalf("Run() = %d, want %d (%s)", code, syncengine.CodeOK, stderr)
	}

	got, err := os.ReadFile(filepath.Join(dst, "b.txt")) //nolint:gosec // the test wrote the file
	if err != nil {
		t.Fatalf("cannot read the copy: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("b.txt = %q, want %q", got, "hello")
	}
}

func TestFilterRulesKeepsTheOrderOfTheFlags(t *testing.T) {
	t.Parallel()

	if _, err := filterRules([]string{
		"--include", "*.png",
		"--exclude", "*",
		"--exclude-from", "patterns.txt",
	}); err == nil {
		t.Fatal("filterRules() = nil, want an error for the missing file")
	}

	rules, err := filterRules([]string{"--exclude=*", "--include=*.png"})
	if err != nil {
		t.Fatalf("filterRules() = %v", err)
	}
	want := []filter.Rule{
		{Kind: filter.Exclude, Pattern: "*"},
		{Kind: filter.Include, Pattern: "*.png"},
	}
	if len(rules) != len(want) {
		t.Fatalf("filterRules() = %v, want %v", rules, want)
	}
	for i, rule := range rules {
		if rule != want[i] {
			t.Errorf("rule %d = %v, want %v", i, rule, want[i])
		}
	}
}

func TestParseSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  int64
	}{
		{value: "", want: 0},
		{value: "1024", want: 1024},
		{value: "10K", want: 10 * 1024},
		{value: "2M", want: 2 * 1024 * 1024},
		{value: "1G", want: 1024 * 1024 * 1024},
		{value: "1.5K", want: 1536},
		{value: "9223372036854774784", want: 9223372036854774784},
		{value: "9223372036854775807", want: math.MaxInt64},
		{value: "9007199254740991K", want: 9223372036854774784},
	}
	for _, tc := range tests {
		got, err := parseSize(tc.value)
		if err != nil {
			t.Errorf("parseSize(%q) = %v", tc.value, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSize(%q) = %d, want %d", tc.value, got, tc.want)
		}
	}

	for _, value := range []string{
		"big", "NaN", "Inf", "-1", "1e3",
		// The values that float64 rounds up to 2^63 and that therefore
		// overflow an int64.
		"9223372036854775808", "9223372036854776832", "9223372036854775809G",
		"9007199254740992K",
	} {
		if _, err := parseSize(value); err == nil {
			t.Errorf("parseSize(%q) = nil, want an error", value)
		}
	}
}
