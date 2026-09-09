package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotEnv reads simple KEY=VALUE lines from path and sets them into the
// process env if they aren't already set (real env vars always win — same
// semantics as the twelve-factor "env overrides file" rule).
//
// Format:
//   - Blank lines and lines starting with '#' are ignored.
//   - Optional surrounding quotes on the value are stripped.
//   - Values may contain '=' — we only split on the first one.
//   - No variable interpolation. No exports. No multi-line strings.
//     We keep this deliberately minimal; use a real loader if you need more.
//
// Missing file is not an error — silence is intentional so prod deploys that
// use real env vars don't need a placeholder file.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i < 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		val = strings.Trim(val, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue // real env wins
		}
		if err := os.Setenv(key, val); err != nil {
			return err
		}
	}
	return sc.Err()
}
