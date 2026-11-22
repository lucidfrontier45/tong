package filter

import "testing"

func TestMatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		rules []Rule
		path  string
		isDir bool
		want  bool
	}{
		{
			name: "no rules keeps everything",
			path: "a/b.txt",
			want: true,
		},
		{
			name:  "exclude by suffix",
			rules: []Rule{{Kind: Exclude, Pattern: "*.log"}},
			path:  "a/b.log",
			want:  false,
		},
		{
			name:  "exclude by suffix keeps other files",
			rules: []Rule{{Kind: Exclude, Pattern: "*.log"}},
			path:  "a/b.txt",
			want:  true,
		},
		{
			name:  "exclude by suffix reaches into directories",
			rules: []Rule{{Kind: Exclude, Pattern: "*.log"}},
			path:  "deep/nested/b.log",
			want:  false,
		},
		{
			name:  "exclude a directory excludes its contents",
			rules: []Rule{{Kind: Exclude, Pattern: "build/"}},
			path:  "build/out.bin",
			want:  false,
		},
		{
			name:  "exclude a directory reaches nested directories",
			rules: []Rule{{Kind: Exclude, Pattern: "build/"}},
			path:  "src/build/out.bin",
			want:  false,
		},
		{
			name:  "a directory only rule keeps files",
			rules: []Rule{{Kind: Exclude, Pattern: "build/"}},
			path:  "build",
			isDir: true,
			want:  false,
		},
		{
			name:  "an anchored pattern only matches from the root",
			rules: []Rule{{Kind: Exclude, Pattern: "a/b"}},
			path:  "a/b/c.txt",
			want:  false,
		},
		{
			name:  "an anchored pattern does not match deeper",
			rules: []Rule{{Kind: Exclude, Pattern: "a/b"}},
			path:  "x/a/b/c.txt",
			want:  true,
		},
		{
			name:  "a leading separator anchors a pattern",
			rules: []Rule{{Kind: Exclude, Pattern: "/a/b"}},
			path:  "x/a/b",
			want:  true,
		},
		{
			name: "the first matching rule wins",
			rules: []Rule{
				{Kind: Include, Pattern: "*.png"},
				{Kind: Exclude, Pattern: "*"},
			},
			path: "a/logo.png",
			want: false,
		},
		{
			name: "an included directory lets the traversal continue",
			rules: []Rule{
				{Kind: Include, Pattern: "*/"},
				{Kind: Include, Pattern: "*.png"},
				{Kind: Exclude, Pattern: "*"},
			},
			path: "a/logo.png",
			want: true,
		},
		{
			name: "an included directory does not include its contents",
			rules: []Rule{
				{Kind: Include, Pattern: "*/"},
				{Kind: Include, Pattern: "*.png"},
				{Kind: Exclude, Pattern: "*"},
			},
			path: "a/notes.txt",
			want: false,
		},
		{
			name:  "an excluded directory takes its contents with it",
			rules: []Rule{{Kind: Exclude, Pattern: "a/b"}},
			path:  "a/b/c.txt",
			want:  false,
		},
		{
			name:  "an excluded directory leaves its siblings alone",
			rules: []Rule{{Kind: Exclude, Pattern: "a/b"}},
			path:  "a/x.txt",
			want:  true,
		},
		{
			name: "the first matching rule wins the other way round",
			rules: []Rule{
				{Kind: Exclude, Pattern: "*"},
				{Kind: Include, Pattern: "*.png"},
			},
			path: "a/logo.png",
			want: false,
		},
		{
			name:  "an include rule makes everything else excluded",
			rules: []Rule{{Kind: Include, Pattern: "*.png"}},
			path:  "a/notes.txt",
			want:  false,
		},
		{
			name:  "a single character wildcard",
			rules: []Rule{{Kind: Exclude, Pattern: "a?.txt"}},
			path:  "ab.txt",
			want:  false,
		},
		{
			name:  "a single character wildcard does not span separators",
			rules: []Rule{{Kind: Exclude, Pattern: "a?"}},
			path:  "a/b",
			want:  true,
		},
		{
			name:  "a character class",
			rules: []Rule{{Kind: Exclude, Pattern: "[abc].txt"}},
			path:  "b.txt",
			want:  false,
		},
		{
			name:  "a negated character class",
			rules: []Rule{{Kind: Exclude, Pattern: "[!abc].txt"}},
			path:  "b.txt",
			want:  true,
		},
		{
			name:  "a negated character class excludes the rest",
			rules: []Rule{{Kind: Exclude, Pattern: "[!abc].txt"}},
			path:  "d.txt",
			want:  false,
		},
		{
			name:  "a double star spans directories",
			rules: []Rule{{Kind: Exclude, Pattern: "a/**/c.txt"}},
			path:  "a/b/c.txt",
			want:  false,
		},
		{
			name:  "a double star spans several directories",
			rules: []Rule{{Kind: Exclude, Pattern: "a/**/c.txt"}},
			path:  "a/b/d/c.txt",
			want:  false,
		},
		{
			name:  "a double star matches nothing in between",
			rules: []Rule{{Kind: Exclude, Pattern: "a/**/c.txt"}},
			path:  "a/c.txt",
			want:  false,
		},
		{
			name:  "a double star on its own matches everything",
			rules: []Rule{{Kind: Exclude, Pattern: "**"}},
			path:  "a/b/c.txt",
			want:  false,
		},
		{
			name:  "a star does not span directories",
			rules: []Rule{{Kind: Exclude, Pattern: "a/*"}},
			path:  "a/b/c.txt",
			want:  false,
		},
		{
			name:  "a star matches a whole path element",
			rules: []Rule{{Kind: Exclude, Pattern: "a/*"}},
			path:  "a/x.txt",
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			matcher, err := New(tc.rules)
			if err != nil {
				t.Fatalf("New() = %v", err)
			}
			if got := matcher.Match(tc.path, tc.isDir); got != tc.want {
				t.Errorf("Match(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
			}
		})
	}
}

func TestNewRejectsEmptyPattern(t *testing.T) {
	t.Parallel()

	if _, err := New([]Rule{{Kind: Exclude, Pattern: ""}}); err == nil {
		t.Fatal("New() = nil, want an error")
	}
}

func TestReadPatterns(t *testing.T) {
	t.Parallel()

	name := t.TempDir() + "/patterns"
	content := "*.log\n\nbuild/\n  spaced.txt  \n"
	if err := writeFile(name, content); err != nil {
		t.Fatalf("cannot write %s: %v", name, err)
	}

	rules, err := ReadPatterns(name)
	if err != nil {
		t.Fatalf("ReadPatterns() = %v", err)
	}

	want := []Rule{
		{Kind: Exclude, Pattern: "*.log"},
		{Kind: Exclude, Pattern: "build/"},
		{Kind: Exclude, Pattern: "spaced.txt"},
	}
	if len(rules) != len(want) {
		t.Fatalf("ReadPatterns() = %v, want %v", rules, want)
	}
	for i, rule := range rules {
		if rule != want[i] {
			t.Errorf("rule %d = %v, want %v", i, rule, want[i])
		}
	}
}
