package exhaustive

import (
	"slices"
	"strconv"

	"github.com/nishanths/exhaustive/passes/enumerated"
	"github.com/nishanths/exhaustive/passes/exhaustive"
	"golang.org/x/tools/go/analysis"

	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/goanalysis"
	"github.com/golangci/golangci-lint/v2/pkg/golinters/internal"
)

func New(settings *config.ExhaustiveSettings) *goanalysis.Linter {
	exhAnalyzer := exhaustive.Analyzer

	var exhCfg, enuCfg map[string]any

	// The analyzer's default value of the "check" flag.
	checks := []string{"switch"}

	if settings != nil {
		if settings.ExplicitExhaustiveSwitch {
			internal.LinterLogger.Warnf("%s: the option 'explicit-exhaustive-switch' has been replaced by 'explicit-exhaustive'.", exhAnalyzer.Name)
		}

		if settings.ExplicitExhaustiveMap {
			internal.LinterLogger.Warnf("%s: the option 'explicit-exhaustive-map' has been replaced by 'explicit-exhaustive'.", exhAnalyzer.Name)
		}

		exhCfg = map[string]any{
			"d":          settings.DefaultSignifiesExhaustive,
			"e":          settings.ExplicitExhaustive || settings.ExplicitExhaustiveSwitch || settings.ExplicitExhaustiveMap,
			"defrequire": settings.DefaultCaseRequired,
			// Should be managed with `linters.exclusions.generated`.
			"g": true,
		}

		if len(settings.Check) > 0 {
			checks = nil

			for _, s := range settings.Check {
				if s == "map" {
					internal.LinterLogger.Warnf("%s: the value 'map' has been replaced by 'mapliteral'.", exhAnalyzer.Name)

					checks = append(checks, "mapliteral")
				} else {
					checks = append(checks, s)
				}
			}

			exhCfg["check"] = checks
		}

		repeatedFlags(exhCfg, "typeonly", settings.OnlyTypes)

		// An empty string is converted to a regular expression that exclude everything.
		if settings.IgnoreEnumMembers != "" {
			internal.LinterLogger.Warnf("%s: the option 'ignore-enum-members' has been replaced by 'ignore-constants'.", exhAnalyzer.Name)

			exhCfg["constignore"] = settings.IgnoreEnumMembers
		}

		repeatedFlags(exhCfg, "constignore", settings.IgnoreConstants)

		// An empty string is converted to a regular expression that exclude everything.
		if settings.IgnoreEnumTypes != "" {
			internal.LinterLogger.Warnf("%s: the option 'ignore-enum-types' has been replaced by 'ignore-types'.", exhAnalyzer.Name)

			exhCfg["typeignore"] = settings.IgnoreEnumTypes
		}

		repeatedFlags(exhCfg, "typeignore", settings.IgnoreTypes)

		enuCfg = map[string]any{
			"p": settings.PackageScopeOnly,
		}
	}

	if !slices.Contains(checks, "typeswitch") {
		exhAnalyzer = withoutFinder(exhAnalyzer)
	}

	return goanalysis.
		NewLinter(
			exhAnalyzer.Name,
			exhAnalyzer.Doc,
			[]*analysis.Analyzer{
				exhAnalyzer,
				enumerated.Analyzer,
			},
			map[string]map[string]any{
				enumerated.Analyzer.Name: enuCfg,
				exhAnalyzer.Name:         exhCfg,
			},
		).
		WithLoadMode(goanalysis.LoadModeTypesInfo)
}

// withoutFinder returns a copy of the analyzer without the finder analyzer in its requirements.
//
// The finder analyzer is only needed by the "typeswitch" check,
// but it exports a fact for every type declaration of every package, including all the dependencies.
// The exhaustive command removes it the same way (see the comment on exhaustive.Analyzer.Requires).
func withoutFinder(a *analysis.Analyzer) *analysis.Analyzer {
	pruned := *a

	pruned.Requires = slices.DeleteFunc(slices.Clone(a.Requires), func(req *analysis.Analyzer) bool {
		// The finder package is internal, so it cannot be compared by pointer.
		return req.Name == "finder"
	})

	return &pruned
}

func repeatedFlags(cfg map[string]any, prefix string, values []string) {
	for i, part := range values {
		cfg[prefix+"-"+strconv.Itoa(i)+"$"] = part
	}
}
