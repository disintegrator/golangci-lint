package exhaustive

import (
	"testing"

	"github.com/nishanths/exhaustive/passes/exhaustive"
	"github.com/stretchr/testify/assert"
	"golang.org/x/tools/go/analysis"
)

func Test_withoutFinder(t *testing.T) {
	original := exhaustive.Analyzer

	pruned := withoutFinder(original)

	assert.Equal(t, []string{"inspect", "enumerated", "finder"}, analyzerNames(original.Requires))
	assert.Equal(t, []string{"inspect", "enumerated"}, analyzerNames(pruned.Requires))

	assert.Equal(t, original.Name, pruned.Name)
	assert.NotNil(t, pruned.Flags.Lookup("check"))
}

func analyzerNames(analyzers []*analysis.Analyzer) []string {
	var names []string
	for _, a := range analyzers {
		names = append(names, a.Name)
	}

	return names
}
