// Package uistatic embeds the UI built by make control-plane.
package uistatic

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var content embed.FS

// Assets returns nil for an API-only Go build without compiled UI assets.
func Assets() fs.FS {
	assets, err := fs.Sub(content, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		return nil
	}
	return assets
}
