// Package web embeds the dashboard's static assets into the binary so the
// service ships as a single file with no runtime disk layout requirements.
//
// The go:embed directive is relative to this file; the whole /dist tree is
// pulled in at compile time. FS() returns an io/fs.FS rooted at "dist" so
// callers don't have to know about the wrapping directory.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded dashboard files, rooted at the dist directory.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Impossible: dist is embedded above.
		panic(err)
	}
	return sub
}
