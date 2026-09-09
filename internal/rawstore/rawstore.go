// Package rawstore persists raw MIME messages to the filesystem.
// Layout: <root>/YYYY/MM/DD/<id>.eml
// Rationale:
//   - Keeps DB small: only metadata + FTS live in SQLite; MIME lives on disk.
//   - Date partitioning bounds directory size and speeds retention sweeps.
//   - Filesystem is the durability layer; write-then-rename gives atomicity.
package rawstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Store writes raw .eml files under Root.
type Store struct {
	Root string
}

// New ensures the root directory exists and returns a Store.
func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir root: %w", err)
	}
	return &Store{Root: root}, nil
}

// Write consumes r and stores it under a partitioned path. Returns the
// absolute path written and the byte count. Safe under concurrent callers
// because we write to a temp file and rename atomically.
func (s *Store) Write(id string, receivedAt time.Time, r io.Reader) (string, int64, error) {
	dir := filepath.Join(s.Root,
		receivedAt.UTC().Format("2006"),
		receivedAt.UTC().Format("01"),
		receivedAt.UTC().Format("02"),
	)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("mkdir partition: %w", err)
	}

	finalPath := filepath.Join(dir, id+".eml")
	tmp, err := os.CreateTemp(dir, id+".*.tmp")
	if err != nil {
		return "", 0, fmt.Errorf("create temp: %w", err)
	}
	// Best-effort cleanup if we fail before rename.
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	n, copyErr := io.Copy(tmp, r)
	if copyErr != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("write body: %w", copyErr)
	}

	// fsync so the bytes are durable before rename — otherwise a crash
	// between rename and flush can leave a zero-length .eml.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("close: %w", err)
	}

	if err := os.Rename(tmpName, finalPath); err != nil {
		return "", 0, fmt.Errorf("rename: %w", err)
	}

	return finalPath, n, nil
}

// WriteAttachment persists an attachment blob under a partitioned tree
// separate from the raw MIME store: attachments/YYYY/MM/DD/<id>.bin.
// Same atomic-write pattern as Write (temp-file + fsync + rename) so a
// crash never leaves a half-written attachment.
func (s *Store) WriteAttachment(id string, receivedAt time.Time, r io.Reader) (string, int64, error) {
	dir := filepath.Join(s.Root, "..", "attachments",
		receivedAt.UTC().Format("2006"),
		receivedAt.UTC().Format("01"),
		receivedAt.UTC().Format("02"),
	)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("mkdir attachments partition: %w", err)
	}

	finalPath := filepath.Join(dir, id+".bin")
	tmp, err := os.CreateTemp(dir, id+".*.tmp")
	if err != nil {
		return "", 0, fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	n, copyErr := io.Copy(tmp, r)
	if copyErr != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("write body: %w", copyErr)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return "", 0, fmt.Errorf("rename: %w", err)
	}
	return finalPath, n, nil
}

// Open returns a reader for the raw MIME at path. Caller closes.
func (s *Store) Open(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

// Delete removes the raw file. Missing files are not an error — the DB row
// might reference a file already sweep-removed by retention.
func (s *Store) Delete(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
