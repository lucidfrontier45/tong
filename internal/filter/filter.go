// Package filter implements the rsync-style include/exclude matcher.
//
// Rules are ordered: the first rule that matches a path decides whether the
// path is transferred. A path is matched against every one of its parent
// directories as well, so that excluding a directory excludes everything
// below it, the way rsync does.
package filter

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Kind tells whether a rule keeps or drops the paths it matches.
type Kind int

const (
	// Include keeps the matching paths.
	Include Kind = iota
	// Exclude drops the matching paths.
	Exclude
)

// Rule is a single include or exclude pattern.
type Rule struct {
	// Kind is either [Include] or [Exclude].
	Kind Kind
	// Pattern is an rsync-style glob.
	Pattern string
}

// Matcher applies an ordered list of rules to paths.
type Matcher struct {
	rules      []rule
	hasInclude bool
}

// rule is a compiled [Rule].
type rule struct {
	include  bool
	dirOnly  bool
	anchored bool
	segments []segment
}

// segment is one compiled path element of a pattern.
type segment struct {
	// doubleStar is set for a "**" element, which spans path elements.
	doubleStar bool
	// re matches a single path element.
	re *regexp.Regexp
}

// New compiles rules into a matcher.
func New(rules []Rule) (*Matcher, error) {
	m := &Matcher{}
	for _, r := range rules {
		compiled, err := compile(r)
		if err != nil {
			return nil, err
		}
		if r.Kind == Include {
			m.hasInclude = true
		}
		m.rules = append(m.rules, compiled)
	}
	return m, nil
}

// Match reports whether the path is transferred. The path is relative to the
// root of the transfer and is always slash-separated. isDir tells whether
// the path itself is a directory.
//
// Every parent directory of the path is matched as well: an excluded
// directory takes its contents with it, the way rsync prunes it. An
// included directory only keeps the traversal going, its contents are
// matched against the rules again.
func (m *Matcher) Match(path string, isDir bool) bool {
	parts := strings.Split(path, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		prefixIsDir := i < len(parts)-1 || isDir
		keep, matched := m.decide(prefix, prefixIsDir)
		if !matched {
			continue
		}
		if i < len(parts)-1 {
			if !keep {
				return false
			}
			continue
		}
		return keep
	}
	// Paths that no rule matches are transferred, unless the user asked for
	// an explicit set of inclusions.
	return !m.hasInclude
}

// Excluded reports whether a rule drops the path. Such files are protected
// from deletion, the way rsync protects them.
func (m *Matcher) Excluded(path string, isDir bool) bool {
	parts := strings.Split(path, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		prefixIsDir := i < len(parts)-1 || isDir
		keep, matched := m.decide(prefix, prefixIsDir)
		if !matched {
			continue
		}
		if i < len(parts)-1 {
			if !keep {
				return true
			}
			continue
		}
		return !keep
	}
	return false
}

// decide returns the verdict of the first rule that matches the path.
func (m *Matcher) decide(path string, isDir bool) (keep, matched bool) {
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.matches(path) {
			return r.include, true
		}
	}
	return false, false
}

// matches reports whether the rule matches the path.
func (r rule) matches(path string) bool {
	parts := strings.Split(path, "/")
	if !r.anchored {
		// A pattern without a separator matches the last element of the
		// path at any depth.
		return matchSegments(r.segments, parts[len(parts)-1:])
	}
	return matchSegments(r.segments, parts)
}

// matchSegments matches path elements against pattern elements.
func matchSegments(pattern []segment, parts []string) bool {
	if len(pattern) == 0 {
		return len(parts) == 0
	}
	if pattern[0].doubleStar {
		for i := range len(parts) + 1 {
			if matchSegments(pattern[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	if !pattern[0].re.MatchString(parts[0]) {
		return false
	}
	return matchSegments(pattern[1:], parts[1:])
}

// compile turns a rule into its compiled form.
func compile(r Rule) (rule, error) {
	pattern := r.Pattern
	if pattern == "" {
		return rule{}, errors.New("empty filter pattern")
	}

	dirOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")

	// A pattern that holds a separator is anchored at the root of the
	// transfer, the way rsync anchors it.
	anchored := strings.Contains(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "" {
		return rule{}, fmt.Errorf("invalid filter pattern %q", r.Pattern)
	}

	elements := strings.Split(pattern, "/")
	segments := make([]segment, 0, len(elements))
	for _, element := range elements {
		if element == "**" {
			segments = append(segments, segment{doubleStar: true})
			continue
		}
		re, err := compileElement(element)
		if err != nil {
			return rule{}, err
		}
		segments = append(segments, segment{re: re})
	}

	return rule{
		include:  r.Kind == Include,
		dirOnly:  dirOnly,
		anchored: anchored,
		segments: segments,
	}, nil
}

// compileElement turns one path element of a pattern into a regexp.
func compileElement(element string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(element); {
		switch c := element[i]; c {
		case '*':
			j := i
			for j < len(element) && element[j] == '*' {
				j++
			}
			if j-i > 1 {
				b.WriteString(".*")
			} else {
				b.WriteString("[^/]*")
			}
			i = j
		case '?':
			b.WriteString("[^/]")
			i++
		case '[':
			class, next, err := parseClass(element, i)
			if err != nil {
				b.WriteString(regexp.QuoteMeta("["))
				i++
				continue
			}
			b.WriteString(class)
			i = next
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("invalid filter pattern %q: %w", element, err)
	}
	return re, nil
}

// parseClass reads a character class starting at element[start]. It returns
// the regexp for the class and the index of the first character after it.
func parseClass(element string, start int) (class string, next int, err error) {
	i := start + 1
	negated := false
	if i < len(element) && (element[i] == '!' || element[i] == '^') {
		negated = true
		i++
	}

	var content strings.Builder
	if i < len(element) && element[i] == ']' {
		content.WriteString("\\]")
		i++
	}
	for i < len(element) {
		switch element[i] {
		case ']':
			if content.Len() == 0 {
				return "", 0, errors.New("empty character class")
			}
			prefix := "["
			if negated {
				prefix += "^"
			}
			return prefix + content.String() + "]", i + 1, nil
		case '\\':
			content.WriteString("\\\\")
		default:
			content.WriteString(string(element[i]))
		}
		i++
	}
	return "", 0, errors.New("unterminated character class")
}

// ReadPatterns reads exclude patterns from a file, one pattern per line.
// Blank lines are ignored.
func ReadPatterns(name string) ([]Rule, error) {
	file, err := os.Open(name) //nolint:gosec // the user asked for this file
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", name, err)
	}
	defer func() { _ = file.Close() }()

	var rules []Rule
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		rules = append(rules, Rule{Kind: Exclude, Pattern: line})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", name, err)
	}
	return rules, nil
}
