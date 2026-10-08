// Package web embeds the local panel assets. No build step, no CDN, no
// external fonts — plain HTML/JS/CSS served by the internal server.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html app.js style.css
var files embed.FS

// FS returns the panel assets rooted at the embedded files.
func FS() fs.FS {
	sub, err := fs.Sub(files, ".")
	if err != nil {
		panic(err) // embed layout is compile-time fixed; unreachable
	}
	return sub
}
