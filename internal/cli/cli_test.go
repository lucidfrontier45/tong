package cli

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		{"--stats", "src", "dst"},
		{"--bwlimit", "100", "src", "dst"},
		{"--remove-source-files", "src", "dst"},
	} {
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		code := Run(context.Background(), args, stdout, stderr)
		if code != syncengine.CodeUsage {
			t.Errorf("Run(%v) = %d, want %d", args, code, syncengine.CodeUsage)
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
