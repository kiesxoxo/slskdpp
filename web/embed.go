// Package web holds the Svelte UI. `npm run build` writes it to dist,
// which is embedded into the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built UI. It holds no index.html when the UI was not
// built before compiling.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
