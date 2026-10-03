package openapi3plan

import (
	"context"
	"fmt"
	"math"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
)

// Constraint is a side-effect-free predicate over a fully assigned set of
// parameter values. It expresses relationships that a single-parameter schema
// cannot (e.g. "a and b cannot both be set" or "mode=X requires tier=2").
//
// It is invoked only on complete cases that already passed ordinary parameter
// validation. It must not mutate its argument, perform I/O, or panic; a panic
// is treated as "constraint false" to keep planning deterministic.
type Constraint func(values map[string]Candidate) bool

// Options controls a planning run.
type Options struct {
	// Domains configures parameters by name. Parameter names are unique within
	// an operation after operation/path-item merging.
	Domains map[string]DomainConfig

	// Constraint is the optional cross-parameter predicate.
	Constraint Constraint

	// Budget bounds the number of complete assignments examined. Zero selects
	// DefaultBudget; a negative value removes the limit.
	Budget int

	// FilterOptions customizes the ordinary parameter validation
	// (openapi3filter.Options). Authentication is never invoked and no HTTP
	// request is sent.
	FilterOptions *openapi3filter.Options
}

// DefaultBudget bounds how many complete assignments are examined unless
// overridden. Pairwise planning targets small finite domains (enums and
// booleans), so this is ample for typical operations.
const DefaultBudget = 100_000

// pairRef identifies one parameter pair and the two candidate positions.
type pairRef struct {
	i, j   int
	ai, aj int
}

// Plan enumerates the finite Cartesian product of the parameter domains,
// checks each complete assignment with the ordinary OpenAPI parameter
// validation rules and the optional constraint, records pairwise feasibility,
// and returns a small deterministic set of valid cases plus a coverage report.
//
// No network access is performed and no authentication function is called.
func Plan(ctx context.Context, route *routers.Route, opts *Options) (*Report, error) {
	if route == nil || route.Operation == nil || route.PathItem == nil {
		return nil, fmt.Errorf("plan requires a route with an operation and path item")
	}
	if opts == nil {
		opts = &Options{}
	}
	budget := opts.Budget
	if budget == 0 {
		budget = DefaultBudget
	}

	method := route.Method
	if method == "" {
		method = "GET"
	}

	merged := mergedParameters(route.Operation, route.PathItem)
	domains := make([]*paramDomain, 0, len(merged))
	for _, param := range merged {
		var cfg DomainConfig
		if opts.Domains != nil {
			cfg = opts.Domains[param.Name]
		}
		d, err := domainFor(param, cfg)
		if err != nil {
			return nil, err
		}
		domains = append(domains, d)
	}

	pairs, offsets := buildPairRefs(domains)
	feasible := make([]bool, len(pairs))

	total := productSize(domains)

	valids := make([]validAssignment, 0)
	examined := 0
	budgetExhausted := false
	var fatal error
	current := make(assignment, len(domains))

	var enumerate func(pos int) bool
	enumerate = func(pos int) bool {
		if budgetExhausted || fatal != nil {
			return false
		}
		if pos == len(domains) {
			if budget >= 0 && examined >= budget {
				budgetExhausted = true
				return false
			}
			examined++
			va, ok, err := evaluate(ctx, route, method, domains, current, opts)
			if err != nil {
				fatal = err
				return false
			}
			if ok {
				valids = append(valids, va)
				markFeasible(domains, offsets, feasible, current)
			}
			return true
		}
		for v := range domains[pos].candidates {
			current[pos] = v
			if !enumerate(pos + 1) {
				return false
			}
		}
		return true
	}
	enumerate(0)
	if fatal != nil {
		return nil, fatal
	}

	return buildReport(domains, pairs, offsets, feasible, valids, examined, total, budgetExhausted)
}

// pairOffsets lets the index of pair (i,j) be found in O(1).
type pairOffsets struct {
	start [][]int // start[i][j] (i<j) is the first pair index for (i,j)
}

func buildPairRefs(domains []*paramDomain) ([]pairRef, pairOffsets) {
	n := len(domains)
	start := make([][]int, n)
	for i := range start {
		start[i] = make([]int, n)
	}
	pairs := make([]pairRef, 0)
	// Order: parameter pair (i,j) ascending, then candidate ai, then aj. This
	// canonical order makes the report stable across input map ordering.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			start[i][j] = len(pairs)
			for ai := range domains[i].candidates {
				for aj := range domains[j].candidates {
					pairs = append(pairs, pairRef{i: i, j: j, ai: ai, aj: aj})
				}
			}
		}
	}
	return pairs, pairOffsets{start: start}
}

// pairIndex returns the index of the pair (i,j,ai,aj).
func (o pairOffsets) index(domains []*paramDomain, i, j, ai, aj int) int {
	return o.start[i][j] + ai*len(domains[j].candidates) + aj
}

// markFeasible marks, in O(n^2), the n choose 2 pairs realized by one valid
// assignment.
func markFeasible(domains []*paramDomain, offsets pairOffsets, feasible []bool, a assignment) {
	for i := 0; i < len(domains); i++ {
		for j := i + 1; j < len(domains); j++ {
			feasible[offsets.index(domains, i, j, a[i], a[j])] = true
		}
	}
}

func productSize(domains []*paramDomain) int64 {
	total := int64(1)
	for _, d := range domains {
		n := int64(len(d.candidates))
		if total > math.MaxInt64/n {
			return math.MaxInt64
		}
		total *= n
	}
	return total
}
