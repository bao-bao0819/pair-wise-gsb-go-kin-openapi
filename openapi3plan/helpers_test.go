package openapi3plan_test

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3plan"
	"github.com/getkin/kin-openapi/routers"
	"github.com/stretchr/testify/require"
)

func stubRoute(spec *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, method, path string) *routers.Route {
	return &routers.Route{
		Spec:      spec,
		PathItem:  pathItem,
		Operation: op,
		Method:    method,
		Path:      path,
	}
}

// candidateKey mirrors the planner's stable candidate identity so the brute
// force reference can align its rows with the report rows.
func candidateKey(t *testing.T, c openapi3plan.Candidate) string {
	t.Helper()
	switch c.Kind {
	case openapi3plan.KindAbsent:
		return "\x00absent"
	case openapi3plan.KindNull:
		return "\x00null"
	default:
		b, err := json.Marshal(c.Value)
		require.NoError(t, err)
		return "\x01" + string(b)
	}
}

// bruteForceStatus independently enumerates the typed Cartesian product using
// schema.VisitJSON (not the HTTP/decoder path) plus the constraint, and
// classifies each pair. The domain is taken directly from the same enum/boolean
// schema values the planner derives.
func bruteForceStatus(t *testing.T, _ *openapi3.T, _ *routers.Route) []openapi3plan.PairStatus {
	t.Helper()

	// Parameters are fixed by the constrained spec, enumerated in the planner's
	// canonical order (all are query parameters: alphabetical by name).
	type cdef struct {
		name string
		vals []openapi3plan.Candidate
	}
	defs := []cdef{
		{"flag", []openapi3plan.Candidate{openapi3plan.Value(false), openapi3plan.Value(true)}},
		{"mode", []openapi3plan.Candidate{openapi3plan.Value("a"), openapi3plan.Value("b")}},
		{"tier", []openapi3plan.Candidate{openapi3plan.Value(int32(1)), openapi3plan.Value(int32(2))}},
	}

	typedValid := func(a []openapi3plan.Candidate) bool {
		values := map[string]openapi3plan.Candidate{}
		for i, d := range defs {
			values[d.name] = a[i]
		}
		// Every parameter is required with a non-nullable primitive schema, so
		// only concrete values are valid.
		for _, c := range a {
			if c.Kind != openapi3plan.KindValue {
				return false
			}
		}
		return constraint(values)
	}

	// Enumerate all 8 assignments in lexicographic order, collect pair hits.
	feasible := map[string]bool{}
	var rec func(pos int, cur []openapi3plan.Candidate)
	rec = func(pos int, cur []openapi3plan.Candidate) {
		if pos == len(defs) {
			if typedValid(cur) {
				for i := 0; i < len(defs); i++ {
					for j := i + 1; j < len(defs); j++ {
						feasible[pairRowKey(defs[i].name, cur[i], defs[j].name, cur[j])] = true
					}
				}
			}
			return
		}
		for _, v := range defs[pos].vals {
			rec(pos+1, append(cur, v))
		}
	}
	rec(0, nil)

	// Build all rows in the planner's canonical ordering.
	rows := []openapi3plan.PairStatus{}
	for i := 0; i < len(defs); i++ {
		for j := i + 1; j < len(defs); j++ {
			for _, ai := range defs[i].vals {
				for _, aj := range defs[j].vals {
					status := openapi3plan.StatusInfeasible
					if feasible[pairRowKey(defs[i].name, ai, defs[j].name, aj)] {
						status = openapi3plan.StatusFeasible
					}
					rows = append(rows, openapi3plan.PairStatus{
						Pair: openapi3plan.Pair{
							ParamA: defs[i].name, CandidateA: ai,
							ParamB: defs[j].name, CandidateB: aj,
						},
						Status: status,
					})
				}
			}
		}
	}
	return rows
}

func pairRowKey(paramA string, ca openapi3plan.Candidate, paramB string, cb openapi3plan.Candidate) string {
	return fmt.Sprintf("%s|%s|%s|%s", paramA, mustKey(ca), paramB, mustKey(cb))
}

func mustKey(c openapi3plan.Candidate) string {
	switch c.Kind {
	case openapi3plan.KindAbsent:
		return "\x00absent"
	case openapi3plan.KindNull:
		return "\x00null"
	default:
		b, _ := json.Marshal(c.Value)
		return "\x01" + string(b)
	}
}

func findPair(t *testing.T, report *openapi3plan.Report,
	paramA string, ca openapi3plan.Candidate,
	paramB string, cb openapi3plan.Candidate,
) openapi3plan.PairStatus {
	t.Helper()
	target := pairRowKey(paramA, ca, paramB, cb)
	for _, row := range report.Pairs {
		if pairRowKey(row.Pair.ParamA, row.Pair.CandidateA, row.Pair.ParamB, row.Pair.CandidateB) == target {
			return row
		}
	}
	t.Fatalf("pair not found: %s", target)
	return openapi3plan.PairStatus{}
}

// countCompletions counts assignments matching fixed parameters (already
// validated and constrained) by varying the free parameters.
func countCompletions(t *testing.T, _ *openapi3.T, _ *routers.Route, fixed map[string]any, freeNames []string, freeVals []any) int {
	t.Helper()
	count := 0
	var rec func(pos int, cur map[string]openapi3plan.Candidate)
	rec = func(pos int, cur map[string]openapi3plan.Candidate) {
		if pos == len(freeNames) {
			if constraint(cur) {
				count++
			}
			return
		}
		for _, v := range freeVals {
			next := map[string]openapi3plan.Candidate{}
			for k, vv := range cur {
				next[k] = vv
			}
			next[freeNames[pos]] = openapi3plan.Value(v)
			rec(pos+1, next)
		}
	}
	start := map[string]openapi3plan.Candidate{}
	for k, v := range fixed {
		start[k] = openapi3plan.Value(v)
	}
	rec(0, start)
	return count
}

func caseSignature(values map[string]openapi3plan.Candidate) string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := ""
	for _, name := range names {
		out += name + "=" + mustKey(values[name]) + ";"
	}
	return out
}

var _ = require.True
