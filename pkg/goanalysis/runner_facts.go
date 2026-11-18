package goanalysis

import (
	"go/types"
	"sync"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

// Facts of the dependencies.
//
// The go/analysis checker copies the facts of every dependency into each action (see inheritFacts in x/tools),
// so every action holds the facts of all its transitive dependencies:
// the time and the memory needed grow quadratically with the size of the import graph.
//
// Instead, an action only holds the facts it produces (about its own package),
// and the facts about another package are read from the action of the same analyzer for that package:
//   - for a direct dependency, from the dependencies of the action (factDeps);
//   - for an indirect dependency, from the factProducers index of the runner.
//
// The facts visible to an action are the same as with the copies:
// the copies were filtered by exportedFrom at each import edge,
// and the filter is applied at lookup time instead (see objectFactVisible).
//
// The facts of a package are only read once all its actions are done,
// because a package is analyzed after all its dependencies.
// They are released by decUse once the package and all its dependents are done.

// factProducers is the index of the actions producing facts:
// the types of a package (*types.Package) are mapped to the actions of the package by analyzer
// (map[*analysis.Analyzer]*action).
type factProducers struct {
	m sync.Map
}

// register adds the actions of the package that produce facts to the index.
// It must be called after the package is loaded,
// because the types of a package loaded from export data are replaced when the package is loaded from source.
func (p *factProducers) register(pkg *packages.Package, actions []*action) {
	if pkg.Types == nil {
		return
	}

	var producers map[*analysis.Analyzer]*action

	for _, act := range actions {
		if len(act.Analyzer.FactTypes) == 0 {
			continue
		}

		if producers == nil {
			producers = make(map[*analysis.Analyzer]*action)
		}

		producers[act.Analyzer] = act
	}

	if producers != nil {
		p.m.Store(pkg.Types, producers)
	}
}

// unregister removes the actions of the package from the index.
func (p *factProducers) unregister(pkg *packages.Package) {
	if pkg.Types != nil {
		p.m.Delete(pkg.Types)
	}
}

// get returns the action of the analyzer that produces the facts about the package, if any.
func (p *factProducers) get(a *analysis.Analyzer, pkg *types.Package) *action {
	producers, ok := p.m.Load(pkg)
	if !ok {
		return nil
	}

	return producers.(map[*analysis.Analyzer]*action)[a]
}

// factProducer returns the action that produces the facts about the package pkg,
// and whether pkg is a direct dependency of the package of act.
// The action is nil if there is no such action, or if pkg is the package of act.
func (act *action) factProducer(pkg *types.Package) (producer *action, direct bool) {
	if dep, ok := act.factDeps[pkg]; ok {
		return dep, true
	}

	return act.runner.factProducers.get(act.Analyzer, pkg), false
}

// objectFactVisible reports whether the facts about obj, an object of a dependency, are visible to act.
//
// When the facts are copied from a dependency to its dependents,
// the facts that cross an import edge are those for which exportedFrom(obj, dep) is true,
// where dep is the imported package.
//   - When obj belongs to a direct dependency, the first edge is enough: dep is the package of obj.
//   - Otherwise, the facts also cross edges where dep is not the package of obj.
//     Then the result of exportedFrom does not depend on dep (only methods, fields, types and constants pass),
//     and is the same as exportedFrom(obj, nil), which implies exportedFrom(obj, obj.Pkg()).
func objectFactVisible(obj types.Object, direct bool) bool {
	if direct {
		return exportedFrom(obj, obj.Pkg())
	}

	return exportedFrom(obj, nil)
}

// objectFactsOf returns the facts that can contain the facts about obj visible to act.
func (act *action) objectFactsOf(obj types.Object) map[objectFactKey]analysis.Fact {
	pkg := obj.Pkg()
	if pkg == act.Package.Types {
		return act.objectFacts
	}

	if pkg == nil {
		return nil
	}

	producer, direct := act.factProducer(pkg)
	if producer == nil || !objectFactVisible(obj, direct) {
		return nil
	}

	return producer.objectFacts
}

// packageFactsOf returns the facts that can contain the facts about the package pkg.
func (act *action) packageFactsOf(pkg *types.Package) map[packageFactKey]analysis.Fact {
	if pkg == act.Package.Types {
		return act.packageFacts
	}

	producer, _ := act.factProducer(pkg)
	if producer == nil {
		return nil
	}

	return producer.packageFacts
}

// forEachDepFactProducer calls fn with the action producing the facts of each (direct or indirect) dependency.
func (act *action) forEachDepFactProducer(fn func(producer *action, direct bool)) {
	// The imports of a package are kept until the end of the analysis (unlike the dependencies of an action).
	seen := map[*packages.Package]bool{act.Package: true}
	stack := []*packages.Package{act.Package}

	for len(stack) > 0 {
		pkg := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, imp := range pkg.Imports {
			if seen[imp] {
				continue
			}

			seen[imp] = true
			stack = append(stack, imp)

			if imp.Types == nil {
				continue
			}

			if producer, direct := act.factProducer(imp.Types); producer != nil {
				fn(producer, direct)
			}
		}
	}
}
