package srcimporter

import (
	"go/build"
	"go/token"
	"go/types"
	"sync"

	"github.com/mdempsky/gocode/gbimporter"
)

const (
	Debug           = false
	EnableVendoring = true
)

var (
	gSharedOnce sync.Once
	gShared     *pkgCache
)

// New returns a types.ImporterFrom that imports packages from source.
func New(ctx *gbimporter.PackedContext, filename string) types.ImporterFrom {
	if ctx == nil {
		c := gbimporter.PackContext(&build.Default)
		ctx = &c
	}
	gSharedOnce.Do(func() {
		gShared = newPkgCache(ctx)
		gShared.BackgroundUpdater()
	})
	return &sharedCache{gShared, ctx, filename}
}

func toPkgCache(imp types.Importer) *pkgCache {
	switch p := imp.(type) {
	case *pkgCache:
		return p
	case *sharedCache:
		return p.pkgCache
	}
	return nil
}

func PkgFileSet(imp types.Importer, pkgPath, srcDir string) *token.FileSet {
	c := toPkgCache(imp)
	if c == nil {
		return nil
	}
	pkg := c.findPackage(pkgPath, srcDir)
	if pkg == nil {
		return nil
	}
	return pkg.fset
}

// sharedCache wraps pkgCache for shared use. Only one Import call is allowed at a time.
type sharedCache struct {
	*pkgCache
	ctx      *gbimporter.PackedContext
	filename string
}

func (p *sharedCache) Import(path string) (*types.Package, error) {
	return p.ImportFrom(path, "", 0)
}

func (p *sharedCache) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	p.pkgCache.mu.Lock()
	defer p.pkgCache.mu.Unlock()
	p.setContext(p.ctx, p.filename)
	return p.pkgCache.ImportFrom(path, srcDir, mode)
}
