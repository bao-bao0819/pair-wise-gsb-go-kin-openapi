package openapi3pairwise

import (
	"context"
	"fmt"
	"sort"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
)

// DefaultBudget bounds how many complete cases Plan validates when
// Planner.Budget is not set.
const DefaultBudget = 100000

// Constraint is a caller-supplied predicate over a complete assignment of
// candidates, keyed by parameter name. It must be free of side effects and
// must not retain or mutate the map. It expresses relations between fields
// that a single-parameter OpenAPI schema cannot express.
type Constraint func(assign map[string]Candidate) bool

// Planner generates pairwise request cases for one operation.
type Planner struct {
	// Route identifies the operation to plan for. Only its Spec, PathItem
	// and Operation fields are consulted; no routing or I/O takes place.
	Route *routers.Route

	// Domains declares the finite candidate domain of every parameter of
	// the operation. Use DomainFor to derive domains from schemas.
	Domains []Domain

	// Constraint, when non-nil, is an additional whole-assignment predicate
	// applied after the ordinary parameter validation rules.
	Constraint Constraint

	// Budget bounds the number of complete-case validations. Pairs whose
	// feasibility cannot be decided within the budget are reported as
	// undetermined. Defaults to DefaultBudget.
	Budget int

	// Options optionally customizes parameter validation. SkipSettingDefaults
	// is always forced on so validation cannot mutate generated requests.
	Options *openapi3filter.Options
}

// Plan enumerates the declared finite domains in canonical order, validates
// each complete case with the ordinary openapi3filter parameter rules plus
// the optional constraint, and then selects a deterministic covering set of
// cases. It never invokes authentication callbacks and never performs I/O.
func (p *Planner) Plan(ctx context.Context) (*Report, error) {
	if p.Route == nil || p.Route.Operation == nil {
		return nil, fmt.Errorf("openapi3pairwise: Planner.Route with an Operation is required")
	}
	budget := p.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}

	params := parametersOf(p.Route)
	names := make([]string, 0, len(params))
	seen := map[string]bool{}
	for _, param := range params {
		if seen[param.Name] {
			return nil, fmt.Errorf("openapi3pairwise: parameter name %q is used by more than one parameter; pairwise planning requires unique names", param.Name)
		}
		seen[param.Name] = true
		names = append(names, param.Name)
	}
	sort.Strings(names)

	declared := map[string][]Candidate{}
	for _, d := range p.Domains {
		if _, ok := declared[d.Param]; ok {
			return nil, fmt.Errorf("openapi3pairwise: duplicate domain for parameter %q", d.Param)
		}
		declared[d.Param] = dedupeSorted(d.Candidates)
	}
	domains := make([][]Candidate, len(names))
	for i, name := range names {
		candidates, ok := declared[name]
		if !ok {
			return nil, fmt.Errorf("openapi3pairwise: no domain declared for parameter %q", name)
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("openapi3pairwise: empty domain for parameter %q", name)
		}
		for _, c := range candidates {
			if c.Kind == ValueConcrete && !isScalar(c.Value) {
				return nil, fmt.Errorf("openapi3pairwise: parameter %q: candidate value of type %T is not a scalar", name, c.Value)
			}
		}
		domains[i] = candidates
		delete(declared, name)
	}
	for name := range declared {
		return nil, fmt.Errorf("openapi3pairwise: domain declared for unknown parameter %q", name)
	}

	// Enumerate complete assignments in canonical (lexicographic) order and
	// keep the feasible ones. The budget caps the number of validations.
	var pool []Case
	evaluations := 0
	exhausted := false
	indices := make([]int, len(names))
	for {
		if evaluations >= budget {
			exhausted = true
			break
		}
		assign := make(map[string]Candidate, len(names))
		for i, name := range names {
			assign[name] = domains[i][indices[i]]
		}
		evaluations++
		if p.validCase(ctx, assign) {
			pool = append(pool, Case{Values: assign})
		}
		// Odometer increment: last parameter varies fastest.
		i := len(indices) - 1
		for ; i >= 0; i-- {
			indices[i]++
			if indices[i] < len(domains[i]) {
				break
			}
			indices[i] = 0
		}
		if i < 0 {
			break
		}
	}

	// All pairs over sorted parameters and canonical candidate order.
	var pairs []Pair
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			for _, a := range domains[i] {
				for _, b := range domains[j] {
					pairs = append(pairs, Pair{Param1: names[i], Value1: a, Param2: names[j], Value2: b})
				}
			}
		}
	}

	// Pairs covered by each feasible case, and vice versa.
	casePairs := make([][]int, len(pool))
	pairCases := make([][]int, len(pairs))
	for ci, c := range pool {
		for pi, pair := range pairs {
			if c.Values[pair.Param1].key() == pair.Value1.key() && c.Values[pair.Param2].key() == pair.Value2.key() {
				casePairs[ci] = append(casePairs[ci], pi)
				pairCases[pi] = append(pairCases[pi], ci)
			}
		}
	}

	// Deterministic covering greedy over the feasible pool. Pool order is
	// canonical because enumeration is canonical, so ties break stably.
	var selected []int
	if len(pairs) == 0 {
		// Fewer than two parameters: no pairs to cover, keep one witness case.
		if len(pool) > 0 {
			selected = append(selected, 0)
		}
	} else {
		uncovered := map[int]bool{}
		for pi := range pairs {
			if len(pairCases[pi]) > 0 {
				uncovered[pi] = true
			}
		}
		for len(uncovered) > 0 {
			best, bestCount := -1, 0
			for ci := range pool {
				count := 0
				for _, pi := range casePairs[ci] {
					if uncovered[pi] {
						count++
					}
				}
				if count > bestCount {
					best, bestCount = ci, count
				}
			}
			if best < 0 {
				break
			}
			selected = append(selected, best)
			for _, pi := range casePairs[best] {
				delete(uncovered, pi)
			}
		}
	}

	// Redundancy elimination: drop any selected case whose pairs stay covered
	// by the remaining selection, scanning in canonical pool order.
	coversAll := func(without int) bool {
		covered := map[int]bool{}
		for _, ci := range selected {
			if ci == without {
				continue
			}
			for _, pi := range casePairs[ci] {
				covered[pi] = true
			}
		}
		for pi := range pairs {
			if len(pairCases[pi]) > 0 && !covered[pi] {
				return false
			}
		}
		return true
	}
	for {
		removed := false
		for _, ci := range selected {
			if len(selected) > 1 && coversAll(ci) {
				next := make([]int, 0, len(selected)-1)
				for _, other := range selected {
					if other != ci {
						next = append(next, other)
					}
				}
				selected = next
				removed = true
				break
			}
		}
		if !removed {
			break
		}
	}
	sort.Ints(selected)

	report := &Report{
		Evaluations: evaluations,
		Exhausted:   exhausted,
	}
	caseIndex := map[int]int{}
	for _, ci := range selected {
		caseIndex[ci] = len(report.Cases)
		report.Cases = append(report.Cases, pool[ci])
	}
	for pi, pair := range pairs {
		pr := PairReport{Pair: pair, Case: -1}
		switch {
		case len(pairCases[pi]) == 0:
			if exhausted {
				pr.Status = PairUndetermined
			} else {
				pr.Status = PairInfeasible
			}
		default:
			pr.Status = PairFeasible
			for _, ci := range pairCases[pi] {
				if idx, ok := caseIndex[ci]; ok {
					pr.Case = idx
					break
				}
			}
		}
		report.Pairs = append(report.Pairs, pr)
	}
	return report, nil
}

// validCase checks a complete assignment with the ordinary parameter
// validation rules and the optional whole-assignment constraint.
func (p *Planner) validCase(ctx context.Context, assign map[string]Candidate) bool {
	input, err := (Case{Values: assign}).BuildRequest(p.Route)
	if err != nil {
		return false
	}
	options := &openapi3filter.Options{}
	if p.Options != nil {
		*options = *p.Options
	}
	options.SkipSettingDefaults = true
	input.Options = options
	for _, param := range parametersOf(p.Route) {
		if err := openapi3filter.ValidateParameter(ctx, input, param); err != nil {
			return false
		}
	}
	if p.Constraint != nil && !p.Constraint(assign) {
		return false
	}
	return true
}
