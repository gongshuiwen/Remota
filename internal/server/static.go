package server

import (
	"io/fs"
	"strings"

	webui "remota.local/remota/web"
)

func consoleFile(path string) string {
	if path == "/" {
		return "dist/index.html"
	}
	if !strings.HasPrefix(path, "/assets/") || !fs.ValidPath(path[1:]) {
		return ""
	}
	name := "dist" + path
	info, err := fs.Stat(webui.Files, name)
	if err != nil || info.IsDir() {
		return ""
	}
	return name
}
