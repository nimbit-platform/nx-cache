package web

import (
	"io/fs"
)

func Static() (fs.FS, error) {
	return fs.Sub(staticRoot, "static")
}
