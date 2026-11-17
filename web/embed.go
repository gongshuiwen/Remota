// Package webui embeds the built console without including frontend sources.
package webui

import "embed"

// Files requires a complete frontend build before compiling the server.
//
//go:embed dist/index.html dist/assets
var Files embed.FS
