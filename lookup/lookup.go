package lookup

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mdempsky/gocode/srcimporter"
)

type cursorSearch struct {
	cursor int
	fset   *token.FileSet

	id         *ast.Ident    // set if cursor is at identifier
	call       *ast.CallExpr // set if within a call context
	importPath string        // set if cursor is at import path
}

func nodeIntersects(offset int, fset *token.FileSet, node ast.Node) bool {
	if node == nil {
		return false
	}
	return fset.Position(node.Pos()).Offset <= offset && offset <= fset.Position(node.End()).Offset
}
func (c *cursorSearch) Visit(node ast.Node) (w ast.Visitor) {
	if !nodeIntersects(c.cursor, c.fset, node) {
		return nil
	}
	switch n := node.(type) {
	case *ast.Ident:
		c.id = n
		return nil
	case *ast.CallExpr:
		if c.fset.Position(n.Lparen).Offset < c.cursor {
			c.call = n
		}
	case *ast.ImportSpec:
		c.importPath = n.Path.Value
		return nil
	}

	return c
}

type callSearch struct {
	id *ast.Ident
}

func (c *callSearch) Visit(node ast.Node) (w ast.Visitor) {
	if node == nil {
		return nil
	}
	switch n := node.(type) {
	case *ast.Ident:
		c.id = n
	default:
		c.id = nil
	}
	return c
}

type Result struct {
	Position token.Position
	Name     string
	Type     string
	Doc      string
	CallArg  int // if this is a call, the index of the argument at the cursor
}

func peekDoc(pos token.Position) string {
	file, err := os.Open(pos.Filename)
	if err != nil {
		log.Printf("Fail open: %v", err)
		return ""
	}
	defer file.Close()
	var peeksize = 100000
	var offset int64
	if pos.Offset > peeksize {
		offset = int64(pos.Offset)
	} else {
		peeksize = pos.Offset
	}
	var data = make([]byte, peeksize)
	n, err := file.ReadAt(data, offset)
	var doc []string
	if n > 0 {
		lines := bytes.Split(data, []byte("\n"))
		if len(lines) > 0 {
			lines = lines[:len(lines)-1]
			i := len(lines) - 1
			for i >= 0 {
				txt := strings.TrimSpace(string(lines[i]))
				if !strings.HasPrefix(txt, "//") {
					break
				}
				i--
				doc = append(doc, txt)
			}
		}
	}
	for i, j := 0, len(doc)-1; i < j; i, j = i+1, j-1 {
		doc[i], doc[j] = doc[j], doc[i] // reverse
	}
	return strings.Join(doc, "\n")
}
func lookupObject(importer types.Importer, thisPkg *types.Package, thisFset *token.FileSet, obj types.Object) Result {
	qualify := func(p *types.Package) string {
		if thisPkg == p {
			return ""
		}
		return p.Name()
	}
	result := Result{CallArg: -1}
	if obj != nil {
		if obj.Pkg() != nil && obj.Pkg().Path() != "" {
			// Find FileSet used by importer.
			fset := srcimporter.PkgFileSet(importer, obj.Pkg().Path(), thisPkg.Path())
			if fset != nil {
				result.Position = fset.Position(obj.Pos())
			}
		} else {
			result.Position = thisFset.Position(obj.Pos())
		}
		result.Type = types.TypeString(obj.Type(), qualify)
		result.Name = obj.Name()
	}
	if result.Position.Filename != "" {
		result.Doc = peekDoc(result.Position)
	}
	return result
}

func Lookup(importer types.Importer, filename string, data []byte, cursor int) (id Result, call Result) {
	fset := token.NewFileSet()
	fileAST, _ := parser.ParseFile(fset, filename, data, parser.AllErrors)

	var otherASTs []*ast.File
	for _, otherName := range findOtherPackageFiles(filename, fileAST.Name.Name) {
		ast, _ := parser.ParseFile(fset, otherName, nil, 0)
		otherASTs = append(otherASTs, ast)
	}

	var cfg types.Config
	cfg.Importer = importer
	cfg.Error = func(err error) {}
	info := types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	pkg, err := cfg.Check("", fset, append(otherASTs, fileAST), &info)

	_, _ = pkg, err
	cs := cursorSearch{cursor: cursor, fset: fset}
	ast.Walk(&cs, fileAST)

	if cs.id != nil {
		id = lookupObject(importer, pkg, fset, info.ObjectOf(cs.id))
	}
	if cs.call != nil {
		var callid callSearch
		ast.Walk(&callid, cs.call.Fun)
		if callid.id != nil {
			call = lookupObject(importer, pkg, fset, info.ObjectOf(callid.id))
			call.CallArg = 0
			for i, arg := range cs.call.Args {
				if arg != nil {
					if cs.fset.Position(arg.End()).Offset < cursor {
						call.CallArg = i + 1
					}
				}
			}
		}
	}
	return id, call
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
	isTestFile := strings.HasSuffix(file, "_test.go")

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
		if !isTestFile && strings.HasSuffix(name, "_test.go") {
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
