package reporterrors

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"io/ioutil"
	"path/filepath"
	"strings"
)

const maxErrors = 100

type Error struct {
	Line, Col int
	Msg       string
}

func toErr(e *scanner.Error) Error {
	return Error{e.Pos.Line, e.Pos.Column, e.Msg}
}
func typesErr(filename string, e types.Error) *Error {
	p := e.Fset.Position(e.Pos)
	if filename == p.Filename {
		return &Error{p.Line, p.Column, e.Msg}
	}
	return nil
}
func Report(importer types.Importer, filename string, data []byte) (reports []Error) {
	fset := token.NewFileSet()
	fileAST, err := parser.ParseFile(fset, filename, data, parser.AllErrors)
	if err != nil {
		if errors, ok := err.(scanner.ErrorList); ok {
			for _, e := range errors {
				reports = append(reports, toErr(e))
			}
		}
		return
	}
	var otherASTs []*ast.File
	for _, otherName := range findOtherPackageFiles(filename, fileAST.Name.Name) {
		ast, _ := parser.ParseFile(fset, otherName, nil, 0)
		otherASTs = append(otherASTs, ast)
	}

	var cfg types.Config
	cfg.Importer = importer
	cfg.Error = func(err error) {
		if e, ok := err.(types.Error); ok {
			if e := typesErr(filename, e); e != nil {
				if len(reports) < maxErrors {
					reports = append(reports, *e)
				}
			}
		}
	}
	cfg.Check("", fset, append(otherASTs, fileAST), nil)
	return
}

//
// The following was copied from suggest.go.
//
func findOtherPackageFiles(filename, pkgName string) []string {
	if filename == "" {
		return nil
	}

	dir, file := filepath.Split(filename)
	dents, err := ioutil.ReadDir(dir)
	if err != nil {
		panic(err)
	}

	// TODO(mdempsky): Use go/build.(*Context).MatchFile or
	// something to properly handle build tags?
	var out []string
	for _, dent := range dents {
		name := dent.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		if name == file || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		abspath := filepath.Join(dir, name)
		if pkgNameFor(abspath) == pkgName {
			out = append(out, abspath)
		}
	}

	return out
}

func pkgNameFor(filename string) string {
	file, _ := parser.ParseFile(token.NewFileSet(), filename, nil, parser.PackageClauseOnly)
	return file.Name.Name
}
