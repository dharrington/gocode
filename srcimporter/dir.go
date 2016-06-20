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
	importName string
	pkgName    string
	modTime    time.Time
}

// dir remembers directory information.
type dir struct {
	cache *pkgCache
	path  string
	// packages contains only packages that have been imported.
	packages  map[string]*pkgInfo
	filePeeks map[string]*filePeek
	peekTime  time.Time // time when updatePeek was called
}

func newDir(cache *pkgCache, path string) *dir {
	return &dir{
		cache:     cache,
		path:      path,
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
			if fi, err := os.Stat(filepath.Join(d.path, i.Name())); err == nil {
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
			pkgName, importName, err := pkgNameFor(d.cache.ext, filename)
			peek.modTime = mt
			peek.pkgName = pkgName
			peek.importName = importName
			if err != nil {
				log.Printf("pkgNameFor err=%v", err)
			}
		}
	}
	return changed, nil
}
func (d *dir) modifiedPackages(mods map[*pkgInfo]bool) {
	for _, peek := range d.filePeeks {
		if pkg := d.packages[peek.importName]; pkg != nil {
			if pkg.updateTime.Before(peek.modTime) {
				mods[pkg] = true
			}
		}
	}
}
func (d *dir) getPackage(importName, pkgPath string) *pkgInfo {
	if p := d.packages[importName]; p != nil {
		return p
	}
	d.parsePackage(importName, pkgPath, 0)
	return d.packages[importName]
}
func (d *dir) parsePackage(importName, pkgPath string, mode parser.Mode) {
	if time.Since(d.peekTime) > time.Second {
		d.updatePeek()
	}
	var packageFiles []string
	var pkgName string
	for fname, p := range d.filePeeks {
		if p.importName == importName {
			packageFiles = append(packageFiles, fname)
			pkgName = p.pkgName
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
		path: pkgPath,
		fset: &token.FileSet{},
		dir:  d,
	}
	d.packages[importName] = pkg
	for _, fname := range packageFiles {
		filename := filepath.Join(d.path, fname)
		if src, err := parser.ParseFile(pkg.fset, filename, nil, mode); err == nil {
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

func pkgNameFor(ext extension, filename string) (pkgName, impName string, err error) {
	pkgName, err = scanPkg(filename)
	if err != nil || pkgName == "" {
		return "", "", err
	}
	return pkgName, ext.ImportName(pkgName, filename), nil
}
