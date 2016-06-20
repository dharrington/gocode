package suggest

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"io/ioutil"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"unsafe"

	"github.com/mdempsky/gocode/lookdot"
)

type Suggester struct {
	debug bool
}

func New(debug bool) *Suggester {
	return &Suggester{
		debug: debug,
	}
}

// Suggest returns a list of suggestion candidates and the length of
// the text that should be replaced, if any.
func (c *Suggester) Suggest(importer types.Importer, filename string, data []byte, cursor int, filter bool) ([]Candidate, int) {
	if cursor < 0 {
		return nil, 0
	}

	fset, pos, pkg := c.analyzePackage(importer, filename, data, cursor)
	scope := pkg.Scope().Innermost(pos)

	ctx, expr, partial := deduceCursorContext(data, cursor)
	if !filter {
		partial = ""
	}
	b := candidateCollector{
		localpkg: pkg,
		partial:  partial,
		filter:   objectFilters[partial],
	}

	switch ctx {
	case selectContext:
		tv, _ := types.Eval(fset, pkg, pos, expr)
		if lookdot.Walk(&tv, b.appendObject) {
			break
		}

		_, obj := scope.LookupParent(expr, pos)
		if pkgName, isPkg := obj.(*types.PkgName); isPkg {
			c.packageCandidates(pkgName.Imported(), &b)
			break
		}

		return nil, 0

	case compositeLiteralContext:
		tv, _ := types.Eval(fset, pkg, pos, expr)
		if tv.IsType() {
			if _, isStruct := tv.Type.Underlying().(*types.Struct); isStruct {
				c.fieldNameCandidates(tv.Type, &b)
				break
			}
		}

		fallthrough
	default:
		c.scopeCandidates(scope, pos, &b)
	}

	res := b.getCandidates()
	if len(res) == 0 {
		return nil, 0
	}
	return res, len(partial)
}

func (c *Suggester) analyzePackage(importer types.Importer, filename string, data []byte, cursor int) (*token.FileSet, token.Pos, *types.Package) {
	// If we're in trailing white space at the end of a scope,
	// sometimes go/types doesn't recognize that variables should
	// still be in scope there.
	filesemi := bytes.Join([][]byte{data[:cursor], []byte(";"), data[cursor:]}, nil)

	fset := token.NewFileSet()
	fileAST, err := parser.ParseFile(fset, filename, filesemi, parser.AllErrors)
	if err != nil && c.debug {
		logParseError("Error parsing input file (outer block)", err)
	}
	pos := fset.File(fileAST.Pos()).Pos(cursor)

	var otherASTs []*ast.File
	for _, otherName := range c.findOtherPackageFiles(filename, fileAST.Name.Name) {
		ast, err := parser.ParseFile(fset, otherName, nil, 0)
		if err != nil && c.debug {
			logParseError("Error parsing other file", err)
		}
		otherASTs = append(otherASTs, ast)
	}

	var cfg types.Config
	cfg.Importer = importer
	cfg.Error = func(err error) {}
	var info types.Info
	info.Scopes = make(map[ast.Node]*types.Scope)
	pkg, _ := cfg.Check("", fset, append(otherASTs, fileAST), &info)

	// Workaround golang.org/issue/15686.
	for node, scope := range info.Scopes {
		switch node := node.(type) {
		case *ast.RangeStmt:
			for _, name := range scope.Names() {
				setScopePos(scope.Lookup(name).(*types.Var), node.X.End())
			}
		}
	}

	return fset, pos, pkg
}

var varScopePosOffset = func() uintptr {
	sf, ok := reflect.TypeOf((*types.Var)(nil)).Elem().FieldByName("scopePos_")
	if !ok {
		log.Fatal("types.Var has no field scopePos_")
	}
	if sf.Type != reflect.TypeOf(token.NoPos) {
		log.Fatalf("types.Var.scopePos_ has type %v, not token.Pos", sf.Type)
	}
	return sf.Offset
}()

func setScopePos(v *types.Var, pos token.Pos) {
	*(*token.Pos)(unsafe.Pointer(uintptr(unsafe.Pointer(v)) + varScopePosOffset)) = pos
}

func (c *Suggester) fieldNameCandidates(typ types.Type, b *candidateCollector) {
	s := typ.Underlying().(*types.Struct)
	for i, n := 0, s.NumFields(); i < n; i++ {
		b.appendObject(s.Field(i))
	}
}

func (c *Suggester) packageCandidates(pkg *types.Package, b *candidateCollector) {
	c.scopeCandidates(pkg.Scope(), token.NoPos, b)
}

func (c *Suggester) scopeCandidates(scope *types.Scope, pos token.Pos, b *candidateCollector) {
	seen := make(map[string]bool)
	for scope != nil {
		isPkgScope := scope.Parent() == types.Universe
		for _, name := range scope.Names() {
			if seen[name] {
				continue
			}
			obj := scope.Lookup(name)
			if !isPkgScope && obj.Pos() > pos {
				continue
			}
			seen[name] = true
			b.appendObject(obj)
		}
		scope = scope.Parent()
	}
}

func logParseError(intro string, err error) {
	if el, ok := err.(scanner.ErrorList); ok {
		log.Printf("%s:", intro)
		for _, er := range el {
			log.Printf(" %s", er)
		}
	} else {
		log.Printf("%s: %s", intro, err)
	}
}

func (c *Suggester) findOtherPackageFiles(filename, pkgName string) []string {
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
