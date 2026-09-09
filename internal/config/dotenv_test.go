package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "" +
		"# comment line\n" +
		"\n" +
		"MAIL_TEST_A=alpha\n" +
		"MAIL_TEST_B=\"beta with spaces\"\n" +
		"MAIL_TEST_C='gamma'\n" +
		"MAIL_TEST_D=has=equals=in=value\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// clean up any lingering test env vars
	for _, k := range []string{"MAIL_TEST_A", "MAIL_TEST_B", "MAIL_TEST_C", "MAIL_TEST_D"} {
		_ = os.Unsetenv(k)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("load: %v", err)
	}

	cases := map[string]string{
		"MAIL_TEST_A": "alpha",
		"MAIL_TEST_B": "beta with spaces",
		"MAIL_TEST_C": "gamma",
		"MAIL_TEST_D": "has=equals=in=value",
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s: got %q want %q", k, got, want)
		}
	}
}

func TestLoadDotEnvRealEnvWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	_ = os.WriteFile(path, []byte("MAIL_TEST_OVERRIDE=file-value\n"), 0o644)

	t.Setenv("MAIL_TEST_OVERRIDE", "shell-value")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := os.Getenv("MAIL_TEST_OVERRIDE"); got != "shell-value" {
		t.Fatalf("want shell to win, got %q", got)
	}
}

func TestLoadDotEnvMissingIsNoError(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatalf("missing .env should not error: %v", err)
	}
}
