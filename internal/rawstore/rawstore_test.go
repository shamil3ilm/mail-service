package rawstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAndOpen(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	body := "From: a@example.test\r\nTo: b@example.test\r\nSubject: hi\r\n\r\nhello world\r\n"
	when := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	path, n, err := s.Write("msg_123", when, strings.NewReader(body))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != int64(len(body)) {
		t.Fatalf("size: got %d want %d", n, len(body))
	}
	if !strings.Contains(path, filepath.Join("2026", "09", "08")) {
		t.Fatalf("partitioned path missing date: %s", path)
	}

	f, err := s.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	got, err := readAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != body {
		t.Fatalf("body mismatch")
	}

	// No stray temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("stray temp file: %s", e.Name())
		}
	}
}

func TestDeleteMissingIsNoError(t *testing.T) {
	s, _ := New(t.TempDir())
	if err := s.Delete(filepath.Join(s.Root, "nope.eml")); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func readAll(r interface{ Read(p []byte) (int, error) }) (string, error) {
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 256)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return string(buf), nil
			}
			return string(buf), err
		}
	}
}
