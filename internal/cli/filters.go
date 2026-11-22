package cli

import (
	"fmt"
	"strings"

	"github.com/lucidfrontier45/tong/internal/filter"
)

// filterRules collects the include and exclude rules in the order they
// appear on the command line.
//
// rsync applies the first rule that matches, so the order of the flags
// matters. kong keeps the values of --include and --exclude in separate
// lists, which loses that order, so it is recovered from the raw arguments.
func filterRules(args []string) ([]filter.Rule, error) {
	var rules []filter.Rule

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}

		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		switch name {
		case "include", "exclude":
			if !hasValue {
				value, i = next(args, i, "--"+name)
				if value == "" {
					return nil, fmt.Errorf("missing value for --%s", name)
				}
			}
			kind := filter.Exclude
			if name == "include" {
				kind = filter.Include
			}
			rules = append(rules, filter.Rule{Kind: kind, Pattern: value})

		case "exclude-from":
			if !hasValue {
				value, i = next(args, i, "--exclude-from")
				if value == "" {
					return nil, fmt.Errorf("missing value for --exclude-from")
				}
			}
			patterns, err := filter.ReadPatterns(value)
			if err != nil {
				return nil, err
			}
			rules = append(rules, patterns...)
		}
	}

	return rules, nil
}

// next returns the value that follows the flag at index i, together with the
// index of that value.
func next(args []string, i int, flag string) (string, int) {
	if i+1 >= len(args) {
		return "", i
	}
	return args[i+1], i + 1
}
