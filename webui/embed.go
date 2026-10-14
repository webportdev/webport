// Package webui embeds the built browser dashboard. The build output is not
// committed: run "mise run build-webui" (npm ci && npm run build in this
// directory) before compiling, or the embed below fails with
// "pattern dist: no matching files found".
package webui

import (
	"embed"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// Assets returns the built dashboard rooted at its index.html.
func Assets() fs.FS {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return assets
}
