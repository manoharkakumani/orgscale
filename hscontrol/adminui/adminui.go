// Package adminui embeds the built admin console (web/admin, built into
// dist/ with `make admin-ui`) so the orgscale binary serves it itself.
package adminui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS is the built console, rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}

	return sub
}
