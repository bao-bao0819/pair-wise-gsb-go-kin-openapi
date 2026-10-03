// Package openapi3pairwise generates a small set of typed request cases for
// an OpenAPI operation so that every feasible pair of parameter values from
// caller-declared finite domains appears in at least one case.
//
// The planner is fully offline: it never invokes authentication callbacks and
// never sends HTTP requests. Complete cases are checked with the ordinary
// openapi3filter parameter validation rules applied to an in-memory request,
// plus an optional caller-supplied whole-assignment constraint function that
// can express relations between fields that a single-parameter OpenAPI schema
// cannot.
//
// Case selection uses a deterministic covering greedy followed by redundancy
// elimination. It does not claim a globally minimal number of cases, but the
// resulting stable set of cases does not depend on the order in which domains
// or candidates were declared.
package openapi3pairwise

import (
	"encoding/json"
	"fmt"
)

// ValueKind distinguishes a parameter being absent from the request, being
// explicitly null, or carrying a concrete value.
type ValueKind int

const (
	// ValueAbsent means the parameter is not present in the request at all.
	ValueAbsent ValueKind = iota
	// ValueNull means the parameter is present with an empty/null value.
	ValueNull
	// ValueConcrete means the parameter is present with a concrete value.
	ValueConcrete
)

// Candidate is a single declared possibility for one parameter.
type Candidate struct {
	Kind  ValueKind
	Value any // only meaningful when Kind == ValueConcrete
}

// Absent returns the candidate representing a missing parameter.
func Absent() Candidate { return Candidate{Kind: ValueAbsent} }

// Null returns the candidate representing an explicitly null parameter.
func Null() Candidate { return Candidate{Kind: ValueNull} }

// Value returns the candidate representing a concrete scalar value.
func Value(v any) Candidate { return Candidate{Kind: ValueConcrete, Value: v} }

// key returns a canonical, deterministic identity for the candidate.
func (c Candidate) key() string {
	switch c.Kind {
	case ValueAbsent:
		return "absent"
	case ValueNull:
		return "null"
	default:
		if data, err := json.Marshal(c.Value); err == nil {
			return "value:" + string(data)
		}
		return fmt.Sprintf("value:%T:%v", c.Value, c.Value)
	}
}

// String renders the candidate in a human-readable, stable form.
func (c Candidate) String() string {
	switch c.Kind {
	case ValueAbsent:
		return "<absent>"
	case ValueNull:
		return "<null>"
	default:
		return fmt.Sprintf("%v", c.Value)
	}
}

// Domain declares the finite set of candidates for one parameter.
type Domain struct {
	Param      string
	Candidates []Candidate
}

// PairStatus is the feasibility verdict for one parameter value pair.
type PairStatus int

const (
	// PairFeasible means at least one valid complete case contains the pair.
	PairFeasible PairStatus = iota
	// PairInfeasible means the whole declared finite domain product was
	// exhausted and no valid complete case contains the pair.
	PairInfeasible
	// PairUndetermined means the search budget was exhausted before the
	// declared domains could be enumerated completely.
	PairUndetermined
)

func (s PairStatus) String() string {
	switch s {
	case PairFeasible:
		return "feasible"
	case PairInfeasible:
		return "infeasible"
	default:
		return "undetermined"
	}
}

// Pair identifies the combination of one candidate for each of two
// distinct parameters. Param1 sorts before Param2.
type Pair struct {
	Param1 string
	Value1 Candidate
	Param2 string
	Value2 Candidate
}

// PairReport records the verdict for one pair and, when feasible, the index
// of a generated case covering it.
type PairReport struct {
	Pair   Pair
	Status PairStatus
	Case   int // index into Report.Cases; -1 unless Status == PairFeasible
}

// Report is the outcome of Planner.Plan: the selected cases plus the
// per-pair coverage verdicts.
type Report struct {
	// Cases is the stable, deterministically ordered set of request cases.
	Cases []Case
	// Pairs holds one entry per parameter value pair, in canonical order.
	Pairs []PairReport
	// Evaluations is the number of complete-case validations performed.
	Evaluations int
	// Exhausted reports whether the search budget ran out before the
	// declared domain product was enumerated completely.
	Exhausted bool
}

// Counts returns how many pairs are feasible, infeasible and undetermined.
func (r *Report) Counts() (feasible, infeasible, undetermined int) {
	for _, pr := range r.Pairs {
		switch pr.Status {
		case PairFeasible:
			feasible++
		case PairInfeasible:
			infeasible++
		default:
			undetermined++
		}
	}
	return
}
