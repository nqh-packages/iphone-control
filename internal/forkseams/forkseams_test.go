package forkseams

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type seam struct {
	Name    string   `yaml:"name"`
	Package string   `yaml:"package"`
	Symbols []string `yaml:"symbols"`
	// Methods are methods of the package's types, matched by name wherever
	// a selector uses them: a file need not import the package to call one.
	Methods []string `yaml:"methods"`
	// Idents are package-level variables of the package's own directory,
	// which only the allowed files of that directory may name.
	Idents  []string `yaml:"idents"`
	Allowed []string `yaml:"allowed"`
	Fix     string   `yaml:"fix"`
}

type declaration struct {
	Seams []seam `yaml:"seams"`
}

type violation struct {
	File string
	Line int
	Rule string
	What string
	Fix  string
}

func (v violation) String() string {
	return fmt.Sprintf("%s:%d: %s: %s\n  fix: %s", v.File, v.Line, v.Rule, v.What, v.Fix)
}

var skippedDirs = map[string]bool{".git": true, "node_modules": true, "testdata": true, "vendor": true, "build": true}

func modulePath(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(m), nil
		}
	}
	return "", fmt.Errorf("%s/go.mod has no module line", root)
}

func readDeclaration(root string) (declaration, error) {
	var d declaration
	b, err := os.ReadFile(filepath.Join(root, "internal", "forkseams", "seams.yaml"))
	if err == nil {
		err = yaml.Unmarshal(b, &d)
	}
	if len(d.Seams) == 0 && err == nil {
		err = fmt.Errorf("seams.yaml declares no seam")
	}
	return d, err
}

// check returns every violation of the seams declared in root's
// internal/forkseams/seams.yaml, in a stable order.
func check(root string) ([]violation, error) {
	decl, err := readDeclaration(root)
	if err != nil {
		return nil, err
	}
	module, err := modulePath(root)
	if err != nil {
		return nil, err
	}
	var out []violation
	seen := map[string]bool{}
	for _, s := range decl.Seams {
		out = append(out, declarationProblems(root, module, s, seen)...)
	}
	files, err := goFiles(root)
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		for _, s := range decl.Seams {
			out = append(out, useOutsideAllowed(root, module, rel, s)...)
			out = append(out, methodUseOutsideAllowed(root, module, rel, s)...)
			out = append(out, identUseOutsideAllowed(root, module, rel, s)...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

func goFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && skippedDirs[d.Name()] && p != root {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files, err
}

func packageDir(module, pkg string) string {
	return strings.TrimPrefix(strings.TrimPrefix(pkg, module), "/")
}

// declarationProblems checks the declaration itself: it names real files and
// real symbols, and no symbol twice.
func declarationProblems(root, module string, s seam, seen map[string]bool) []violation {
	var out []violation
	problem := func(rule, what string) {
		out = append(out, violation{File: "internal/forkseams/seams.yaml", Line: 1, Rule: rule, What: what, Fix: "correct the entry for " + strconv.Quote(s.Name) + " in seams.yaml"})
	}
	for _, a := range s.Allowed {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(a))); err != nil {
			problem("allowed-file-missing", a+" is allowed but does not exist")
		}
	}
	declared := declaredFuncs(filepath.Join(root, filepath.FromSlash(packageDir(module, s.Package))))
	for _, sym := range s.Symbols {
		key := s.Package + "." + sym
		if seen[key] {
			problem("symbol-declared-twice", key+" is declared in more than one seam")
		}
		seen[key] = true
		if !declared[sym] {
			problem("symbol-missing", sym+" is not declared in package "+s.Package)
		}
	}
	methods := declaredMethods(filepath.Join(root, filepath.FromSlash(packageDir(module, s.Package))))
	for _, m := range s.Methods {
		key := s.Package + ".(method)." + m
		if seen[key] {
			problem("symbol-declared-twice", key+" is declared in more than one seam")
		}
		seen[key] = true
		if !methods[m] {
			problem("symbol-missing", m+" is not a method declared in package "+s.Package)
		}
	}
	vars := declaredVars(filepath.Join(root, filepath.FromSlash(packageDir(module, s.Package))))
	for _, v := range s.Idents {
		key := s.Package + ".(var)." + v
		if seen[key] {
			problem("symbol-declared-twice", key+" is declared in more than one seam")
		}
		seen[key] = true
		if !vars[v] {
			problem("symbol-missing", v+" is not a package-level variable declared in package "+s.Package)
		}
	}
	return out
}

func declaredVars(dir string) map[string]bool {
	out := map[string]bool{}
	pkgs, _ := parser.ParseDir(token.NewFileSet(), dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
					for _, sp := range gd.Specs {
						for _, n := range sp.(*ast.ValueSpec).Names {
							out[n.Name] = true
						}
					}
				}
			}
		}
	}
	return out
}

// identUseOutsideAllowed finds every mention of a seam's package variables
// in a file of the declaring directory that is not allowed to name them.
func identUseOutsideAllowed(root, module, rel string, s seam) []violation {
	if len(s.Idents) == 0 || path.Dir(rel) != packageDir(module, s.Package) || slices.Contains(s.Allowed, rel) {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
	if err != nil {
		return []violation{{File: rel, Line: 1, Rule: "unparsable", What: err.Error(), Fix: "fix the syntax error"}}
	}
	var out []violation
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && slices.Contains(s.Idents, id.Name) {
			out = append(out, violation{File: rel, Line: fset.Position(id.Pos()).Line, Rule: "seam-" + strings.ReplaceAll(s.Name, " ", "-"),
				What: id.Name + " is used outside the files that may use it", Fix: s.Fix})
		}
		return true
	})
	return out
}

func declaredMethods(dir string) map[string]bool {
	out := map[string]bool{}
	pkgs, _ := parser.ParseDir(token.NewFileSet(), dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv != nil {
					out[fn.Name.Name] = true
				}
			}
		}
	}
	return out
}

// methodUseOutsideAllowed finds every selector that names one of a seam's
// methods in a file that is not allowed to use it. Methods have no import to
// follow, so the match is by name: a seam names methods unlikely to be
// called on anything else.
func methodUseOutsideAllowed(root, module, rel string, s seam) []violation {
	if len(s.Methods) == 0 || path.Dir(rel) == packageDir(module, s.Package) || slices.Contains(s.Allowed, rel) {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
	if err != nil {
		return []violation{{File: rel, Line: 1, Rule: "unparsable", What: err.Error(), Fix: "fix the syntax error"}}
	}
	var out []violation
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && slices.Contains(s.Methods, sel.Sel.Name) {
			out = append(out, violation{File: rel, Line: fset.Position(sel.Sel.Pos()).Line, Rule: "seam-" + strings.ReplaceAll(s.Name, " ", "-"),
				What: "." + sel.Sel.Name + " is used outside the files that may use it", Fix: s.Fix})
		}
		return true
	})
	return out
}

func declaredFuncs(dir string) map[string]bool {
	out := map[string]bool{}
	pkgs, _ := parser.ParseDir(token.NewFileSet(), dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
					out[fn.Name.Name] = true
				}
			}
		}
	}
	return out
}

// useOutsideAllowed finds every mention of a seam's symbols in a file that is
// not allowed to make one: a call, a reference, through an import alias or
// a dot import.
func useOutsideAllowed(root, module, rel string, s seam) []violation {
	if path.Dir(rel) == packageDir(module, s.Package) {
		return nil
	}
	for _, a := range s.Allowed {
		if a == rel {
			return nil
		}
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
	if err != nil {
		return []violation{{File: rel, Line: 1, Rule: "unparsable", What: err.Error(), Fix: "fix the syntax error"}}
	}
	name, dot := "", false
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p != s.Package {
			continue
		}
		switch {
		case imp.Name == nil:
			name = path.Base(s.Package)
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			name = imp.Name.Name
		}
	}
	if name == "" && !dot {
		return nil
	}
	symbols := map[string]bool{}
	for _, sym := range s.Symbols {
		symbols[sym] = true
	}
	var out []violation
	report := func(pos token.Pos, sym string) {
		out = append(out, violation{File: rel, Line: fset.Position(pos).Line, Rule: "seam-" + strings.ReplaceAll(s.Name, " ", "-"),
			What: sym + " is used outside the files that may use it", Fix: s.Fix})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && name != "" && x.Name == name && symbols[n.Sel.Name] {
				report(n.Sel.Pos(), name+"."+n.Sel.Name)
			}
			return false
		case *ast.Ident:
			if dot && symbols[n.Name] {
				report(n.Pos(), n.Name)
			}
		}
		return true
	})
	return out
}

func TestForkSeamsHoldInTheRepository(t *testing.T) {
	vs, err := check(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		t.Error("\n" + v.String())
	}
}

func TestForkSeamsGuardCatchesEachRule(t *testing.T) {
	rows := []struct {
		dir, rule, file string
		line            int
	}{
		{"direct", "seam-no-prompt-store", "cmd/other/use.go", 6},
		{"aliased", "seam-no-prompt-store", "cmd/other/use.go", 6},
		{"reference", "seam-no-prompt-store", "cmd/other/use.go", 5},
		{"dotimport", "seam-no-prompt-store", "cmd/other/use.go", 6},
		{"missingfile", "allowed-file-missing", "internal/forkseams/seams.yaml", 1},
		{"twice", "symbol-declared-twice", "internal/forkseams/seams.yaml", 1},
		{"nosymbol", "symbol-missing", "internal/forkseams/seams.yaml", 1},
		{"method", "seam-secret-hand-out", "cmd/other/use.go", 4},
		{"methodmissing", "symbol-missing", "internal/forkseams/seams.yaml", 1},
		{"identuse", "seam-passcode-store-variables", "cmd/pinky/other.go", 4},
		{"identmissing", "symbol-missing", "internal/forkseams/seams.yaml", 1},
		{"codeslot", "seam-code-slot-hand-out", "cmd/other/use.go", 4},
		{"codeslotwrite", "seam-code-slot-writer", "cmd/other/use.go", 4},
		{"interruptfiles", "seam-interrupt-book-user-files", "cmd/other/use.go", 6},
		{"credtrigger", "seam-credential-trigger-writer", "cmd/other/use.go", 4},
		{"iconpictures", "seam-icon-detector-pictures", "cmd/other/use.go", 6},
		{"iconstart", "seam-icon-detector-start", "cmd/other/use.go", 6},
		{"iconwriter", "seam-icon-guesses-writer", "cmd/other/use.go", 4},
	}
	for _, r := range rows {
		vs, err := check(filepath.Join("testdata", r.dir))
		if err != nil {
			t.Fatalf("%s: %v", r.dir, err)
		}
		var found bool
		for _, v := range vs {
			if v.Rule == r.rule && v.File == r.file && v.Line == r.line {
				found = true
				if v.Fix == "" || !strings.Contains(v.String(), "fix: ") {
					t.Errorf("%s: the failure has no fix line: %s", r.dir, v)
				}
			}
		}
		if !found {
			t.Errorf("%s: the guard did not report %s at %s:%d; it reported %v", r.dir, r.rule, r.file, r.line, vs)
		}
	}
}

func TestForkSeamsGuardPassesWhatIsAllowed(t *testing.T) {
	vs, err := check(filepath.Join("testdata", "clean"))
	if err != nil || len(vs) != 0 {
		t.Fatalf("clean fixture: %v, %v", vs, err)
	}
}

func TestForkSeamsAllowedFilesExistAndUseTheSeam(t *testing.T) {
	root := filepath.Join("..", "..")
	decl, err := readDeclaration(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range decl.Seams {
		for _, a := range s.Allowed {
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(a)))
			if err != nil {
				t.Errorf("%s: %v", a, err)
				continue
			}
			var uses bool
			for _, sym := range append(append(slices.Clone(s.Symbols), s.Methods...), s.Idents...) {
				uses = uses || strings.Contains(string(b), sym)
			}
			if !uses {
				t.Errorf("%s is allowed for %q but does not use it: remove it from seams.yaml", a, s.Name)
			}
		}
	}
}
