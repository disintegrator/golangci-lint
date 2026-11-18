package goanalysis

import (
	"fmt"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/types/objectpath"

	"github.com/golangci/golangci-lint/v2/internal/cache"
	"github.com/golangci/golangci-lint/v2/pkg/goanalysis/load"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/timeutils"
)

// The packages used to compare the facts visible to an analyzer
// with the facts visible with the go/analysis checker.
//
//	a <- b <- c <- e
//	a <- d, b <- d
//
// The packages a and b are only dependencies:
// they are loaded from the cache when the cache is warm.
var factsTestFiles = map[string]string{
	"go.mod": "module example.com/facts\n\ngo 1.22\n",
	"a/a.go": `package a

type T struct {
	X int
	y int
}

func (T) M() {}

func (T) m() {}

func (*T) P(v int) int { return v }

type I interface{ IM() }

const C = 1

var V int

func F(p int) T { return T{} }

func f() {}
`,
	"b/b.go": `package b

import "example.com/facts/a"

type U struct {
	a.T
	Z int
}

func G() a.T { return a.T{} }

func H(t a.T) {}

var W = a.V

const D = a.C
`,
	"c/c.go": `package c

import "example.com/facts/b"

func Use() {
	t := b.G()
	t.M()
	_ = t.X
	var u b.U
	_ = u.Z
	b.H(t)
	_ = b.W
}
`,
	"d/d.go": `package d

import (
	"example.com/facts/a"
	"example.com/facts/b"
)

func Use() {
	_ = a.F(0)
	_ = a.V
	_ = a.C
	b.G().M()
	var i a.I
	i.IM()
}
`,
	"e/e.go": `package e

import "example.com/facts/c"

func Use() { c.Use() }
`,
}

type visibleFact struct{ Desc string }

func (*visibleFact) AFact() {}

func (f *visibleFact) String() string { return f.Desc }

// newVisibleFactsAnalyzer returns an analyzer that exports a fact for every object of the package
// (that can be persisted in the cache) and for the package,
// and reports one diagnostic per fact it can see:
// through AllObjectFacts, AllPackageFacts, ImportObjectFact, and ImportPackageFact.
func newVisibleFactsAnalyzer(analyzed *sync.Map) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:      "visiblefacts",
		Doc:       "reports the visible facts",
		FactTypes: []analysis.Fact{new(visibleFact)},
		Run: func(pass *analysis.Pass) (any, error) {
			analyzed.Store(pass.Pkg.Path(), true)

			for _, obj := range pass.TypesInfo.Defs {
				if obj == nil || obj.Pkg() != pass.Pkg {
					continue
				}

				if _, err := objectpath.For(obj); err != nil {
					// The fact would not be persisted in the cache.
					continue
				}

				pass.ExportObjectFact(obj, &visibleFact{Desc: describeObject(obj)})
			}

			pass.ExportPackageFact(&visibleFact{Desc: pass.Pkg.Path()})

			// The runner de-duplicates the diagnostics with the same position and message.
			messages := map[string]bool{}

			for _, f := range pass.AllObjectFacts() {
				messages[fmt.Sprintf("AllObjectFacts: %s: %s", describeObject(f.Object), f.Fact)] = true
			}

			for _, f := range pass.AllPackageFacts() {
				messages[fmt.Sprintf("AllPackageFacts: %s: %s", f.Package.Path(), f.Fact)] = true
			}

			for _, obj := range usedObjects(pass) {
				fact := new(visibleFact)
				found := pass.ImportObjectFact(obj, fact)
				messages[fmt.Sprintf("ImportObjectFact: %s: %t %s", describeObject(obj), found, fact)] = true

				fact = new(visibleFact)
				found = pass.ImportPackageFact(obj.Pkg(), fact)
				messages[fmt.Sprintf("ImportPackageFact: %s: %t %s", obj.Pkg().Path(), found, fact)] = true
			}

			for _, msg := range slices.Sorted(maps.Keys(messages)) {
				pass.Report(analysis.Diagnostic{Pos: pass.Files[0].Package, Message: msg})
			}

			return nil, nil
		},
	}
}

func describeObject(obj types.Object) string {
	return obj.Pkg().Path() + ": " + types.ObjectString(obj, (*types.Package).Name)
}

// usedObjects returns the objects of other packages used by the package,
// and the methods, fields, and parameters reachable from them.
func usedObjects(pass *analysis.Pass) []types.Object {
	c := &objectCollector{pkg: pass.Pkg, seen: map[types.Object]bool{}}

	for _, obj := range pass.TypesInfo.Uses {
		c.add(obj)
	}

	return slices.Collect(maps.Keys(c.seen))
}

type objectCollector struct {
	pkg  *types.Package
	seen map[types.Object]bool
}

func (c *objectCollector) add(obj types.Object) {
	if obj == nil || obj.Pkg() == nil || obj.Pkg() == c.pkg || c.seen[obj] {
		return
	}

	c.seen[obj] = true

	switch obj := obj.(type) {
	case *types.TypeName:
		c.addNamed(obj.Type())

	case *types.Func:
		sig := obj.Signature()

		for v := range sig.Params().Variables() {
			c.add(v)
		}

		for v := range sig.Results().Variables() {
			c.addTypeOf(v.Type())
		}

	case *types.Var:
		c.addTypeOf(obj.Type())
	}
}

// addTypeOf adds the type name of a named type.
func (c *objectCollector) addTypeOf(t types.Type) {
	if named, ok := types.Unalias(t).(*types.Named); ok {
		c.add(named.Obj())
	}
}

// addNamed adds the methods and the fields (or interface methods) of a named type.
func (c *objectCollector) addNamed(t types.Type) {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return
	}

	for m := range named.Methods() {
		c.add(m)
	}

	switch u := named.Underlying().(type) {
	case *types.Struct:
		for f := range u.Fields() {
			c.add(f)
		}

	case *types.Interface:
		for m := range u.Methods() {
			c.add(m)
		}
	}
}

func TestMain(m *testing.M) {
	// Isolate the cache used by the tests.
	dir, err := os.MkdirTemp("", "golangci-lint-cache-")
	if err != nil {
		panic(err)
	}

	os.Setenv("GOLANGCI_LINT_CACHE", dir)

	code := m.Run()

	_ = os.RemoveAll(dir)

	os.Exit(code)
}

func Test_runner_facts(t *testing.T) {
	dir := t.TempDir()

	nonce := strconv.FormatInt(time.Now().UnixNano(), 10)

	writeFactsTestFiles(t, dir, nonce, "")

	patterns := []string{"./c", "./d", "./e"}

	t.Run("cold cache", func(t *testing.T) {
		analyzed := assertSameFactsAsChecker(t, dir, patterns)

		// The dependencies are analyzed from source.
		assert.ElementsMatch(t, []string{"a", "b", "c", "d", "e"}, analyzed)
	})

	t.Run("warm cache", func(t *testing.T) {
		analyzed := assertSameFactsAsChecker(t, dir, patterns)

		// The facts of the dependencies are loaded from the cache.
		assert.ElementsMatch(t, []string{"c", "d", "e"}, analyzed)
	})

	t.Run("modified dependency", func(t *testing.T) {
		// The file hashes are memoized by file name for the process lifetime,
		// so the modified module is written in another directory.
		// The cache keys only use the paths relative to the module.
		dir := t.TempDir()

		writeFactsTestFiles(t, dir, nonce, "b/b.go")

		analyzed := assertSameFactsAsChecker(t, dir, patterns)

		// The facts of a are loaded from the cache, b is analyzed from source.
		assert.ElementsMatch(t, []string{"b", "c", "d", "e"}, analyzed)
	})
}

// assertSameFactsAsChecker runs the visiblefacts analyzer with the go/analysis checker and with the runner,
// checks that the reported facts are the same,
// and returns the names of the packages analyzed by the runner.
func assertSameFactsAsChecker(t *testing.T, dir string, patterns []string) []string {
	t.Helper()

	want := runVisibleFactsWithChecker(t, dir, patterns)
	require.NotEmpty(t, want)

	var analyzed sync.Map

	got := runVisibleFactsWithRunner(t, dir, patterns, newVisibleFactsAnalyzer(&analyzed))

	assert.Equal(t, want, got)

	var names []string
	analyzed.Range(func(key, _ any) bool {
		names = append(names, filepath.Base(key.(string)))
		return true
	})

	return names
}

func runVisibleFactsWithChecker(t *testing.T, dir string, patterns []string) map[string][]string {
	t.Helper()

	cfg := &packages.Config{Mode: packages.LoadAllSyntax, Dir: dir}

	pkgs, err := packages.Load(cfg, patterns...)
	require.NoError(t, err)
	require.Zero(t, packages.PrintErrors(pkgs))

	var analyzed sync.Map

	graph, err := checker.Analyze([]*analysis.Analyzer{newVisibleFactsAnalyzer(&analyzed)}, pkgs, nil)
	require.NoError(t, err)

	reported := map[string][]string{}

	for _, act := range graph.Roots {
		require.NoError(t, act.Err)

		for _, diag := range act.Diagnostics {
			reported[act.Package.PkgPath] = append(reported[act.Package.PkgPath], diag.Message)
		}
	}

	for _, messages := range reported {
		slices.Sort(messages)
	}

	return reported
}

func runVisibleFactsWithRunner(t *testing.T, dir string, patterns []string, analyzer *analysis.Analyzer) map[string][]string {
	t.Helper()

	// Same mode as the package loader of golangci-lint for go/analysis linters with types information.
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedModule |
			packages.NeedImports | packages.NeedDeps | packages.NeedExportFile | packages.NeedTypesSizes,
		Dir: dir,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	require.NoError(t, err)
	require.Zero(t, packages.PrintErrors(pkgs))

	loadGuard := load.NewGuard()
	fset := token.NewFileSet()

	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		pkg.Fset = fset
		loadGuard.AddMutexForPkg(pkg)
	})

	log := logutils.NewStderrLog(logutils.DebugKeyGoAnalysis)
	sw := timeutils.NewStopwatch("test", log)

	pkgCache, err := cache.NewCache(sw, log)
	require.NoError(t, err)

	r := newRunner("test", log, pkgCache, loadGuard, LoadModeTypesInfo, sw)

	diags, errs, _ := r.run([]*analysis.Analyzer{analyzer}, pkgs)
	require.Empty(t, errs)

	reported := map[string][]string{}

	for _, diag := range diags {
		reported[diag.Pkg.PkgPath] = append(reported[diag.Pkg.PkgPath], diag.Message)
	}

	for _, messages := range reported {
		slices.Sort(messages)
	}

	return reported
}

// writeFactsTestFiles writes the test module in dir.
// The nonce makes the packages unknown to the cache, the modified file gets an extra comment.
func writeFactsTestFiles(t *testing.T, dir, nonce, modified string) {
	t.Helper()

	for name, content := range factsTestFiles {
		if filepath.Ext(name) == ".go" {
			content += "\n// " + nonce + "\n"
		}

		if name == modified {
			content += "\n// modified\n"
		}

		filename := filepath.Join(dir, filepath.FromSlash(name))

		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		require.NoError(t, os.WriteFile(filename, []byte(content), 0o600))
	}
}
