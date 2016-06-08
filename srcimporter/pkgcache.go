package srcimporter

import (
	"errors"
	"go/ast"
	"go/token"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mdempsky/gocode/gbimporter"
)

type pkgInfo struct {
	*ast.Package
	path        string // path on which package was imported; eg go/types
	fset        *token.FileSet
	dir         *dir
	tpkg        *types.Package
	typesCached bool // have types been computed
	updateTime  time.Time
}

func (p *pkgInfo) Imports() []*pkgInfo {
	var imppkg []*pkgInfo
	for _, ip := range packageImports(p.Package) {
		ipp := p.PkgCache().findPackage(ip, p.dir.path)
		if ipp != nil {
			imppkg = append(imppkg, ipp)
		}
	}
	return imppkg
}

func (p *pkgInfo) PkgCache() *pkgCache {
	return p.dir.cache
}

func (p *pkgInfo) Types() *types.Package {
	if p.typesCached {
		return p.tpkg
	}
	p.typesCached = true
	// Import all dep packages or types will be missing.
	for _, impPath := range packageImports(p.Package) {
		p.PkgCache().Import(impPath)
	}
	cfg := types.Config{
		Error:                    func(err error) {}, // don't stop after error
		IgnoreFuncBodies:         true,
		DisableUnusedImportCheck: true,
		FakeImportC:              true,
		Importer:                 p.PkgCache(),
	}
	p.tpkg = types.NewPackage(p.path, p.Package.Name)

	ch := types.NewChecker(&cfg, p.fset, p.tpkg, nil)
	var files []*ast.File
	for name, f := range p.Package.Files {
		if i := strings.LastIndex(name, "/"); i != -1 {
			name = name[i+1:]
		}
		if defBuildContext.goodOSArchFile(name, nil) {
			files = append(files, f)
		}
	}
	ch.Files(files)
	return p.tpkg
}

// pkgCache implements types.ImporterFrom by parsing go files. pkgCache is
// designed to be reused repeatedly. Modified source files are detected in
// the background to force package reloading.
type pkgCache struct {
	// mu is acquired by the background updater goroutine before mutating
	// pkgCache. Callers should lock mu before calling pkgCache methods.
	mu          sync.Mutex
	dirs        map[string]*dir     // absolute path -> dir
	vendorPaths map[string][]string // path -> vendor dir list
	gopath      []string
	currentFile string
	lastUse     time.Time // last time Import was called
	done        chan struct{}
}

func newPkgCache(ctx *gbimporter.PackedContext) *pkgCache {
	cache := &pkgCache{
		dirs:        make(map[string]*dir),
		vendorPaths: make(map[string][]string),
		done:        make(chan struct{}),
	}
	return cache
}

func (p *pkgCache) setContext(paths []string, filename string) {
	if !stringSliceEq(p.gopath, paths) {
		if Debug {
			log.Printf("Set gopath: %v", paths)
		}
		p.gopath = paths
		p.dirs = make(map[string]*dir)
		p.vendorPaths = make(map[string][]string)
	}
	p.currentFile = filename
}

// Returns a list of vendor paths accessible from sources in srcDir.
// Caches information, so adding a vendor directory later will not be
// correctly handled.
func (p *pkgCache) getVendorPaths(srcDir string) []string {
	if vp, ok := p.vendorPaths[srcDir]; ok {
		return vp
	}
	idx := strings.LastIndex(srcDir, "/")
	if idx == -1 {
		return nil
	}
	vendor := filepath.Join(srcDir, "vendor")
	fi, err := os.Stat(vendor)
	baseVendor := p.getVendorPaths(srcDir[:idx])
	if err == nil && fi.IsDir() {
		p.vendorPaths[srcDir] = append([]string{vendor}, baseVendor...)
	} else {
		p.vendorPaths[srcDir] = baseVendor
	}
	return p.vendorPaths[srcDir]
}

// removeStalePackages checks for stale packages and removes them.
func (p *pkgCache) removeStalePackages() {
	if Debug {
		t0 := time.Now()
		defer func() {
			log.Printf("removeStalePackages took %v", time.Since(t0))
		}()
	}
	lastCheckpoint := time.Now()
	// Frequently unlock mutex to prevent blocking for too long.
	checkpoint := func() {
		if time.Since(lastCheckpoint) > time.Millisecond {
			p.mu.Unlock()
			lastCheckpoint = time.Now()
			time.Sleep(time.Microsecond)
			p.mu.Lock()
		}
	}
	// Copy dir paths locally.
	p.mu.Lock()
	defer p.mu.Unlock()
	var paths []string
	for path := range p.dirs {
		paths = append(paths, path)
	}

	// TODO: This is fairly long and inefficient. This entire routine
	// can take over a second even if nothing changes. Reloads due to packages
	// that once failed to import is ugly.

	// Gather all imported paths.
	allImports := map[string]bool{}
	for _, path := range paths {
		d := p.dirs[path]
		if d == nil {
			continue
		}
		checkpoint()
		for _, pkg := range d.packages {
			for _, imp := range packageImports(pkg.Package) {
				allImports[imp] = true
			}
		}
	}
	delete(allImports, "unsafe") // unsafe isn't loaded from disk
	// Gather missing imports. Packages that previously failed to import
	// need special attention if they show up later.
	newPackages := map[string]bool{}
	for path := range allImports {
		checkpoint()
		if p.findPackage(path, "") == nil {
			if pkg := p.getPackage(path, ""); pkg != nil {
				newPackages[path] = true
			}
		}
	}

	// If any previously-failed-to-import packages import now, mark dependencies as modified.
	modifiedPackages := map[*pkgInfo]bool{}
	if len(newPackages) > 0 {
		for _, path := range paths {
			d := p.dirs[path]
			if d == nil {
				continue
			}
			checkpoint()
			for _, pkg := range d.packages {
				for _, imp := range packageImports(pkg.Package) {
					if newPackages[imp] {
						modifiedPackages[pkg] = true
					}
				}
			}
		}
	}

	// Detect modified packages.
	for _, path := range paths {
		d := p.dirs[path]
		if d == nil {
			continue
		}
		checkpoint()
		d.updatePeek()
		d.modifiedPackages(modifiedPackages)
	}

	// There is no back-link between dependencies. Instead, loop over all
	// packages to determine if they use the modified packages directly.
	dependentPackages := map[*pkgInfo]bool{}
	for len(modifiedPackages) > 0 {
		for _, path := range paths {
			checkpoint()
			d := p.dirs[path]
			if d == nil {
				continue
			}
			for _, pkg := range d.packages {
				for _, imp := range pkg.Imports() {
					if modifiedPackages[imp] {
						dependentPackages[pkg] = true
					}
				}
			}
		}
		for pkg := range modifiedPackages {
			if pkg.dir.packages[pkg.Name] != nil {
				pkg.dir.unlink()
			}
		}
		// Repeat. dependentPackages are indirectly modified.
		modifiedPackages = dependentPackages
		dependentPackages = map[*pkgInfo]bool{}
	}
}

// BackgroundUpdater begins a background goroutine to refresh stale packages.
func (p *pkgCache) BackgroundUpdater() {
	go func() {
		for {
			select {
			case <-p.done:
				return
			default:
				// If not idle, remove stale packages.
				p.mu.Lock()
				active := time.Since(p.lastUse) < 10*time.Minute
				p.mu.Unlock()
				if active {
					p.removeStalePackages()
				}
			}
			time.Sleep(5 * time.Second)
		}
	}()
}

func (p *pkgCache) Close() error {
	close(p.done)
	return nil
}

// Import implements types.Importer
func (p *pkgCache) Import(path string) (*types.Package, error) {
	return p.ImportFrom(path, "", 0)
}

// Import implements types.ImporterFrom
func (p *pkgCache) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	p.lastUse = time.Now()
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	pkg := p.getPackage(path, srcDir)
	if pkg == nil {
		return nil, errors.New("no package")
	}
	return pkg.Types(), nil
}

func (p *pkgCache) getDir(path string) *dir {
	if d := p.findDir(path); d != nil {
		return d
	}
	d := newDir(p, path)
	d.updatePeek()
	p.dirs[d.path] = d
	return d
}

func (p *pkgCache) findDir(path string) *dir {
	if d, ok := p.dirs[path]; ok {
		return d
	}
	return nil
}

var failedPackages = map[string]bool{}

func (p *pkgCache) lookupPaths(pkgPath, srcDir string) (name string, paths []string) {
	name = pkgPath
	pkgDir := ""
	if idx := strings.LastIndex(pkgPath, "/"); idx != -1 {
		name = pkgPath[idx+1:]
		pkgDir = pkgPath[:idx]
	}
	if EnableVendoring {
		for _, d := range p.getVendorPaths(srcDir) {
			paths = append(paths, filepath.Join(d, pkgPath))
		}
	}
	for _, d := range p.gopath {
		paths = append(paths, filepath.Join(d, pkgPath))
	}
	extendLookupPaths(p, pkgDir, pkgPath, &paths)
	return name, paths
}

func (p *pkgCache) findPackage(pkgPath, srcDir string) *pkgInfo {
	name, paths := p.lookupPaths(pkgPath, srcDir)
	for _, pp := range paths {
		sd := p.findDir(pp)
		if sd == nil {
			continue
		}
		if pkg := sd.packages[name]; pkg != nil {
			return pkg
		}
	}
	return nil
}
func (p *pkgCache) getPackage(pkgPath, srcDir string) *pkgInfo {
	name, paths := p.lookupPaths(pkgPath, srcDir)
	for _, pp := range paths {
		sd := p.getDir(pp)
		if sd == nil {
			continue
		}
		if pkg := sd.getPackage(name, pkgPath); pkg != nil {
			return pkg
		}
	}
	if !failedPackages[pkgPath] {
		failedPackages[pkgPath] = true
		if Debug {
			log.Printf("GetPackage FAIL: %v\nPaths=%v", pkgPath, paths)
		}
	}
	return nil
}
