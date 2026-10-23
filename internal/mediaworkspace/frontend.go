package mediaworkspace

import (
	"embed"
	"io/fs"
)

//go:embed ui/*
var frontend embed.FS

// Frontend exposes the media module's own assets. The desktop shell supplies its
// DOM mount points and host callbacks; this module does not start a web service.
func Frontend() fs.FS {
	files, err := fs.Sub(frontend, "ui")
	if err != nil {
		panic(err) // ui is a compile-time embedded directory.
	}
	return files
}
