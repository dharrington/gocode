package srcimporter

import (
	"path/filepath"
	"strings"

	"github.com/mdempsky/gocode/gbimporter"
)

var makeExtension = func() extension { return &defaultExtension{} }

// These functions exist to allow environment-specific customization.
type extension interface {
	// return true to indicate no change in context
	SetContext(ctx *gbimporter.PackedContext, filename string) bool
	LookupPaths(p *pkgCache, srcDir, pkgDir, pkgPath string) []string
	ImportName(pkgName, fileName string) string
}

type defaultExtension struct {
	gopath []string
}

func (e *defaultExtension) SetContext(ctx *gbimporter.PackedContext, filename string) bool {
	var paths []string
	for _, p := range append([]string{ctx.GOROOT}, filepath.SplitList(ctx.GOPATH)...) {
		if p != "" {
			p = filepath.Join(p, "src/")
			if isDir(p) {
				paths = append(paths, p)
			}
		}
	}
	if strings.Join(paths, ";") != strings.Join(e.gopath, ";") {
		e.gopath = paths
		return false
	}
	return true
}

func (e *defaultExtension) LookupPaths(p *pkgCache, srcDir, pkgDir, pkgPath string) []string {
	var paths []string
	if EnableVendoring {
		for _, d := range p.getVendorPaths(srcDir) {
			paths = append(paths, filepath.Join(d, pkgPath))
		}
	}
	for _, d := range e.gopath {
		paths = append(paths, filepath.Join(d, pkgPath))
	}
	return paths
}

func (d *defaultExtension) ImportName(pkgName, fileName string) string {
	return filepath.Base(filepath.Dir(fileName))
}
