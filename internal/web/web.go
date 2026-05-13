package web

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var distFS embed.FS

// Dist returns a filesystem rooted at the SvelteKit build output.
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "build")
	if err != nil {
		panic(err)
	}
	return sub
}
