package sqlite

import "os"

// mkdirAll is split into its own file so it's easy to swap for tests.
func mkdirAll(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// ftsBodyMaxBytes caps the amount of body text we shove into FTS5. Longer
// messages still work — we just don't index the tail. 32 KB is enough to
// cover a typical mail body while keeping the index compact.
const ftsBodyMaxBytes = 32 * 1024

// truncateForFTS keeps the first N bytes of s, taking care not to split a
// UTF-8 rune. Empty input returns empty (avoids writing "" that FTS treats
// as a null token).
func truncateForFTS(s string) string {
	if len(s) <= ftsBodyMaxBytes {
		return s
	}
	// Back off to the last UTF-8 rune boundary at or before ftsBodyMaxBytes.
	i := ftsBodyMaxBytes
	for i > 0 && (s[i]&0xC0) == 0x80 {
		i--
	}
	return s[:i]
}
