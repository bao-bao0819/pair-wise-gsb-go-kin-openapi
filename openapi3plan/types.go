package openapi3plan

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Kind classifies how a parameter value is supplied in a planned request.
type Kind int

const (
	// KindAbsent means the parameter is not present in the request at all.
	KindAbsent Kind = iota
	// KindNull means the parameter is present but carries a null/empty value.
	// Over the wire query/cookie parameters serialize as an empty segment;
	// whether that decodes to a schema null depends on the parameter schema
	// (e.g. non-string types with allowEmptyValue, or a nullable schema).
	KindNull
	// KindValue means the parameter carries the concrete Candidate.Value.
	KindValue
)

func (k Kind) String() string {
	switch k {
	case KindAbsent:
		return "absent"
	case KindNull:
		return "null"
	case KindValue:
		return "value"
	default:
		return "unknown"
	}
}

// Candidate is a single, finite value that a parameter may take.
//
// The zero Value is valid for KindAbsent and KindNull. KindValue requires a
// non-nil, JSON-serializable, comparable Value (a primitive or a slice of
// primitives); maps and other infinite/unsupported shapes are rejected.
type Candidate struct {
	Kind  Kind
	Value any
}

// Absent returns the canonical "parameter missing" candidate.
func Absent() Candidate { return Candidate{Kind: KindAbsent} }

// Null returns the canonical "parameter present but null" candidate.
func Null() Candidate { return Candidate{Kind: KindNull} }

// Value wraps a concrete value as a candidate.
func Value(v any) Candidate { return Candidate{Kind: KindValue, Value: v} }

// key returns a stable, comparable identifier of the candidate. Concrete
// values are keyed by their JSON encoding; absent and null have reserved
// prefixes that JSON literals cannot collide with.
func (c Candidate) key() (string, error) {
	switch c.Kind {
	case KindAbsent:
		return "\x00absent", nil
	case KindNull:
		return "\x00null", nil
	case KindValue:
		if c.Value == nil {
			return "", fmt.Errorf("concrete candidate has nil value; use Null() instead")
		}
		b, err := json.Marshal(c.Value)
		if err != nil {
			return "", fmt.Errorf("candidate value is not JSON-serializable: %w", err)
		}
		return "\x01" + string(b), nil
	}
	return "", fmt.Errorf("unknown candidate kind %d", c.Kind)
}

// assignment gives, for each parameter index, the index of its chosen
// candidate within that parameter's sorted domain.
type assignment []int

// assignmentKey is the canonical, stable identity of a complete case.
func assignmentKey(a assignment, keys [][]string) string {
	key := make([]byte, 0, 64)
	for i, idx := range a {
		key = append(key, keys[i][idx]...)
		key = append(key, 0x1f)
	}
	return string(key)
}

// Pair identifies a value pair between two parameters.
type Pair struct {
	ParamA     string
	CandidateA Candidate
	ParamB     string
	CandidateB Candidate
}

// Status classifies a pair with respect to the declared finite domains and
// the validation/constraint rules.
type Status int

const (
	// StatusUndetermined means the search budget was exhausted before the
	// finite domains could be exhausted, so feasibility is not proven either
	// way.
	StatusUndetermined Status = iota
	// StatusInfeasible means every completion in the declared finite product
	// was examined within budget and none passed validation and constraint.
	StatusInfeasible
	// StatusFeasible means at least one complete, valid case covers the pair.
	StatusFeasible
)

func (s Status) String() string {
	switch s {
	case StatusFeasible:
		return "feasible"
	case StatusInfeasible:
		return "infeasible"
	case StatusUndetermined:
		return "undetermined"
	default:
		return "unknown"
	}
}

// PairStatus is one row of the coverage report.
type PairStatus struct {
	Pair   Pair
	Status Status
}

// Case is one generated, typed request case.
//
// Values maps every operation parameter name to its chosen candidate; a
// parameter carrying KindAbsent is omitted from Request. Request is built
// per-case and is ready for openapi3filter.ValidateRequest (or
// openapi3filter.ValidateParameter) against Route.
type Case struct {
	Values  map[string]Candidate
	Request *http.Request
	// Pairs are the parameter value pairs this case exercises.
	Pairs []Pair
}

// CoveredPairs returns the parameter value pairs this case exercises.
func (c *Case) CoveredPairs() []Pair { return c.Pairs }

// Report describes the planning outcome.
type Report struct {
	// Cases are the selected, non-redundant, valid cases in deterministic
	// order.
	Cases []*Case
	// Pairs lists every declared pair in canonical order with its status.
	Pairs []PairStatus

	// Count of complete assignments examined (bounded by Budget).
	Examined int
	// Total size of the declared finite Cartesian product (capped at
	// math.MaxInt64).
	Total int64
	// BudgetExhausted is true when examination stopped before Total.
	BudgetExhausted bool
	// Valid is the number of complete assignments that passed parameter
	// validation and the constraint.
	Valid int

	// Feasible / Infeasible / Undetermined count the rows of Pairs.
	Feasible      int
	Infeasible    int
	Undetermined  int
	Covered       int
	FeasiblePairs []PairStatus // subset of Pairs with StatusFeasible
}
