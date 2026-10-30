// Package web embeds the built SPA (web/dist) into the dash binary.
package web

import "embed"

// Dist holds the output of `npm run build`. dist/.gitkeep is committed so the package
// compiles before the first build.
//
//go:embed all:dist
var Dist embed.FS
