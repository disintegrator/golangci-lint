package goanalysis

import (
	"go/token"
	"go/types"
	"reflect"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

type testFact struct{ Name string }

func (*testFact) AFact() {}

func Test_inheritFacts_noAllocationPerFact(t *testing.T) {
	if isFactsInheritDebug {
		t.Skip("facts inherit debug is enabled")
	}

	pkg := types.NewPackage("example.com/dep", "dep")

	dep := &action{
		Package:      &packages.Package{Types: pkg},
		objectFacts:  map[objectFactKey]analysis.Fact{},
		packageFacts: map[packageFactKey]analysis.Fact{},
	}

	factType := reflect.TypeFor[*testFact]()

	for i := range 1000 {
		obj := types.NewConst(token.NoPos, pkg, "C"+strconv.Itoa(i), types.Typ[types.Int], nil)
		dep.objectFacts[objectFactKey{obj: obj, typ: factType}] = &testFact{Name: obj.Name()}
	}

	dep.packageFacts[packageFactKey{pkg: pkg, typ: factType}] = &testFact{Name: pkg.Path()}

	act := &action{
		objectFacts:  map[objectFactKey]analysis.Fact{},
		packageFacts: map[packageFactKey]analysis.Fact{},
	}

	// The first call fills the maps of act, the next calls only overwrite existing entries.
	inheritFacts(act, dep)

	assert.Len(t, act.objectFacts, 1000)
	assert.Len(t, act.packageFacts, 1)

	allocs := testing.AllocsPerRun(10, func() {
		inheritFacts(act, dep)
	})

	assert.Zero(t, allocs)
}
