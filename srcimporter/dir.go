package srcimporter

import (
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// filePeek remembers basic file information.
type filePeek struct {
	pkgName string
	modTime time.Time
}

// dir remembers directory information.
type dir struct {
	cache  *pkgCache
	path   string
	suffix string
	// packages contains only packages that have been imported.
	packages  map[string]*pkgInfo
	filePeeks map[string]*filePeek
	peekTime  time.Time // time when updatePeek was called
}

func newDir(cache *pkgCache, root string, pkgPath string) *dir {
	return &dir{
		cache:     cache,
		path:      filepath.Join(root, pkgPath),
		suffix:    pkgPath,
		packages:  map[string]*pkgInfo{},
		filePeeks: map[string]*filePeek{},
	}
}
func (d *dir) modTime() time.Time {
	var latest time.Time
	fd, err := os.Open(d.path)
	if err != nil {
		return latest
	}
	defer fd.Close()

	list, err := fd.Readdir(-1)
	if err != nil {
		return latest
	}
	for _, i := range list {
		if strings.HasSuffix(i.Name(), ".go") && !i.IsDir() {
			if fi, err := os.Stat(d.path + "/" + i.Name()); err == nil {
				if t := fi.ModTime(); latest.Before(t) {
					latest = t
				}
			}
		}
	}
	return latest
}

func filterPkgFile(path os.FileInfo) bool {
	return !strings.HasSuffix(path.Name(), "_test.go")
}

func (d *dir) updatePeek() (changed bool, err error) {
	d.peekTime = time.Now()
	fd, err := os.Open(d.path)
	if err != nil {
		return false, err
	}
	defer fd.Close()

	list, err := fd.Readdir(-1)
	if err != nil {
		return false, err
	}
	for _, entry := range list {
		fileName := entry.Name()
		if strings.HasSuffix(fileName, ".go") && filterPkgFile(entry) {
			peek := d.filePeeks[fileName]
			if peek == nil {
				peek = &filePeek{}
				d.filePeeks[fileName] = peek
			}
			mt := entry.ModTime()
			if mt == peek.modTime {
				continue // already up to date!
			}
			d.filePeeks[fileName].modTime = mt
			changed = true
			filename := filepath.Join(d.path, fileName)
			pkgName, err := pkgNameFor(filename)
			if err == nil {
				peek.modTime = mt
				peek.pkgName = pkgName
			} else {
				log.Printf("pkgNameFor err=%v", err)
			}
		}
	}
	return changed, nil
}
func (d *dir) modifiedPackages(mods map[*pkgInfo]bool) {
	for _, peek := range d.filePeeks {
		if pkg := d.packages[peek.pkgName]; pkg != nil {
			if pkg.updateTime.Before(peek.modTime) {
				mods[pkg] = true
			}
		}
	}
}
func (d *dir) getPackage(pkgName string) *pkgInfo {
	if p := d.packages[pkgName]; p != nil {
		return p
	}
	d.parsePackage(pkgName, 0)
	return d.packages[pkgName]
}
func (d *dir) parsePackage(pkgName string, mode parser.Mode) {
	if time.Since(d.peekTime) > time.Second {
		d.updatePeek()
	}
	var packageFiles []string
	for fname, p := range d.filePeeks {
		if p.pkgName == pkgName {
			packageFiles = append(packageFiles, fname)
		}
	}
	if len(packageFiles) == 0 {
		return
	}
	pkg := &pkgInfo{
		Package: &ast.Package{
			Name:  pkgName,
			Files: make(map[string]*ast.File),
		},
		fset: &token.FileSet{},
		dir:  d,
	}
	d.packages[pkgName] = pkg
	for _, fname := range packageFiles {
		filename := filepath.Join(d.path, fname)
		if src, err := parser.ParseFile(pkg.fset, filename, nil, mode); err == nil {
			if name := src.Name.Name; name != pkg.Package.Name {
				log.Printf("Package name mismatch: %q!=%q", name, pkg.Package.Name)
			}
			pkg.Files[filename] = src
		} else {
			log.Printf("ParseFile: %v", err)
		}
		pkg.updateTime = time.Now()
	}
}

func (d *dir) unlink() {
	delete(d.cache.dirs, d.path)
}

func pkgNameFor(filename string) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.PackageClauseOnly)
	if err != nil {
		return "", err
	}
	return file.Name.Name, nil
}
