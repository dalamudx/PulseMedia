package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var distEmbedFS embed.FS

// Dist returns an http.FileSystem rooted at the dist directory.
func Dist() http.FileSystem {
	sub, err := fs.Sub(distEmbedFS, "dist")
	if err != nil {
		return http.FS(distEmbedFS)
	}
	return http.FS(sub)
}
