// Package assets provides embedded static assets for Orgscale.
// All static files (favicon, CSS, SVG) are embedded here for
// centralized asset management.
package assets

import (
	_ "embed"
)

// Favicon is the embedded favicon.png file served at /favicon.ico
//
//go:embed favicon.png
var Favicon []byte

// CSS is the embedded style.css stylesheet used in HTML templates.
// Contains Material for MkDocs design system styles.
//
//go:embed style.css
var CSS string

// SVG is the embedded orgscale.svg logo used in HTML templates.
//
//go:embed orgscale.svg
var SVG string
