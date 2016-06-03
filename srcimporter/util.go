package srcimporter

import (
	"go/ast"
	"os"
	"strings"
)

func isDir(path string) bool {
	if fi, err := os.Stat(path); err == nil {
		return fi.IsDir()
	}
	return false
}
func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// packageImports returns all paths imported by p.
func packageImports(p *ast.Package) []string {
	paths := make(map[string]bool)
	for _, f := range p.Files {
		for _, imp := range f.Imports {
			if imp.Path != nil {
				paths[imp.Path.Value] = true
			}
		}
	}
	ps := []string{}
	for p := range paths {
		ps = append(ps, strings.Trim(p, "\""))
	}
	return ps
}
