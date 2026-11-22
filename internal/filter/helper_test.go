package filter

import "os"

// writeFile writes a file, for the tests.
func writeFile(name, content string) error {
	return os.WriteFile(name, []byte(content), 0o600)
}
