package provider

import "os"

// osHostname is a thin wrapper for testability; the SMTP relay uses it in
// its EHLO greeting.
func osHostname() (string, error) { return os.Hostname() }
