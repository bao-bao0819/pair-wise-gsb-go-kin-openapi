package openapi3pairwise

import (
	"context"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/stretchr/testify/require"
)

// testRoute builds an operation whose three query parameters are related by
// cross-field constraints that no single-parameter schema can express:
//
//	mode=a  forbids level=3
//	mode=b  forbids level=2
//	level=3 requires flag=true
//
// Consequently the pair (mode=b, level=3) has exactly one feasible completion
// (flag=true) and the pair (mode=a, level=3) has none.
func testRoute() (*routers.Route, Constraint) {
	mode := openapi3.NewQueryParameter("mode")
	mode.Required = true
	mode.Schema = openapi3.NewSchemaRef("", openapi3.NewStringSchema().WithEnum("a", "b"))

	level := openapi3.NewQueryParameter("level")
	level.Required = true
	level.Schema = openapi3.NewSchemaRef("", openapi3.NewIntegerSchema().WithEnum(float64(1), float64(2), float64(3)))

	flag := openapi3.NewQueryParameter("flag")
	flag.Schema = openapi3.NewSchemaRef("", openapi3.NewBoolSchema())

	doc := &openapi3.T{OpenAPI: "3.0.3", Info: &openapi3.Info{Title: "t", Version: "1"}}
	op := &openapi3.Operation{
		Parameters: openapi3.Parameters{
			{Value: mode},
			{Value: level},
			{Value: flag},
		},
		Responses: openapi3.NewResponses(),
	}
	route := &routers.Route{
		Spec:      doc,
		Method:    "GET",
		Path:      "/items",
		PathItem:  &openapi3.PathItem{},
		Operation: op,
	}

	constraint := func(assign map[string]Candidate) bool {
		mode := assign["mode"].Value.(string)
		level := assign["level"].Value.(float64)
		if mode == "a" && level == 3 {
			return false
		}
		if mode == "b" && level == 2 {
			return false
		}
		if level == 3 {
			flag := assign["flag"]
			return flag.Kind == ValueConcrete && flag.Value == true
		}
		return true
	}
	return route, constraint
}

func testDomains(t *testing.T, route *routers.Route) []Domain {
	t.Helper()
	params := parametersByName(route)
	modeDom, err := DomainFor(params["mode"], nil)
	require.NoError(t, err)
	levelDom, err := DomainFor(params["level"], nil)
	require.NoError(t, err)
	flagDom, err := DomainFor(params["flag"], nil)
	require.NoError(t, err)
	return []Domain{
		{Param: "mode", Candidates: modeDom},
		{Param: "level", Candidates: levelDom},
		{Param: "flag", Candidates: flagDom},
	}
}

// exhaustiveFeasible brute-forces the whole declared domain product as an
// independent reference, returning the feasible complete assignments.
func exhaustiveFeasible(t *testing.T, route *routers.Route, domains []Domain, constraint Constraint) []map[string]Candidate {
	t.Helper()
	planner := &Planner{Route: route, Domains: domains, Constraint: constraint}
	var out []map[string]Candidate
	var visit func(i int, assign map[string]Candidate)
	visit = func(i int, assign map[string]Candidate) {
		if i == len(domains) {
			if planner.validCase(context.Background(), assign) {
				out = append(out, assign)
			}
			return
		}
		for _, cand := range domains[i].Candidates {
			next := make(map[string]Candidate, len(domains))
			for k, v := range assign {
				next[k] = v
			}
			next[domains[i].Param] = cand
			visit(i+1, next)
		}
	}
	visit(0, map[string]Candidate{})
	return out
}

func findPair(t *testing.T, report *Report, param1, value1, param2, value2 string) PairReport {
	t.Helper()
	for _, pr := range report.Pairs {
		if pr.Pair.Param1 == param1 && pr.Pair.Param2 == param2 &&
			pr.Pair.Value1.String() == value1 && pr.Pair.Value2.String() == value2 {
			return pr
		}
	}
	t.Fatalf("pair (%s=%s, %s=%s) not found in report", param1, value1, param2, value2)
	return PairReport{}
}

func referencePairCount(reference []map[string]Candidate, pair Pair) int {
	count := 0
	for _, assign := range reference {
		if assign[pair.Param1].key() == pair.Value1.key() && assign[pair.Param2].key() == pair.Value2.key() {
			count++
		}
	}
	return count
}

func TestPlannerThreeParamConstraint(t *testing.T) {
	route, constraint := testRoute()
	domains := testDomains(t, route)

	// Derived domains: enum extraction, boolean extraction, Absent for the
	// optional parameter.
	require.Equal(t, []Candidate{Value("a"), Value("b")}, domains[0].Candidates)
	require.Equal(t, []Candidate{Value(float64(1)), Value(float64(2)), Value(float64(3))}, domains[1].Candidates)
	require.Equal(t, []Candidate{Absent(), Value(false), Value(true)}, domains[2].Candidates)

	report, err := (&Planner{Route: route, Domains: domains, Constraint: constraint}).Plan(context.Background())
	require.NoError(t, err)
	require.False(t, report.Exhausted)
	require.NotEmpty(t, report.Cases)

	// The exhaustive reference over the small declared domains: the whole
	// 2x3x3 product was evaluated and 10 complete assignments are feasible.
	reference := exhaustiveFeasible(t, route, domains, constraint)
	require.Len(t, reference, 10)
	require.Equal(t, 18, report.Evaluations)

	// The planner produces strictly fewer cases than the exhaustive product.
	require.Less(t, len(report.Cases), len(reference))
	require.Equal(t, 7, len(report.Cases))

	// The pair (mode=b, level=3) has exactly one feasible completion.
	single := findPair(t, report, "level", "3", "mode", "b")
	require.Equal(t, PairFeasible, single.Status)
	require.Equal(t, 1, referencePairCount(reference, single.Pair))
	covering := report.Cases[single.Case]
	require.Equal(t, Value(true), covering.Values["flag"])

	// The pair (mode=a, level=3) has no feasible completion at all.
	none := findPair(t, report, "level", "3", "mode", "a")
	require.Equal(t, PairInfeasible, none.Status)
	require.Equal(t, -1, none.Case)
	require.Equal(t, 0, referencePairCount(reference, none.Pair))

	// Every pair verdict matches the exhaustive reference, and every
	// feasible pair is covered by the case the report names for it.
	feasible, infeasible, undetermined := report.Counts()
	require.Equal(t, 0, undetermined)
	require.Equal(t, 17, feasible)
	require.Equal(t, 4, infeasible)
	refFeasible, refInfeasible := 0, 0
	for _, pr := range report.Pairs {
		refCount := referencePairCount(reference, pr.Pair)
		if refCount > 0 {
			refFeasible++
			require.Equal(t, PairFeasible, pr.Status, "pair %+v", pr.Pair)
			require.GreaterOrEqual(t, pr.Case, 0)
			c := report.Cases[pr.Case]
			require.Equal(t, pr.Pair.Value1.key(), c.Values[pr.Pair.Param1].key())
			require.Equal(t, pr.Pair.Value2.key(), c.Values[pr.Pair.Param2].key())
		} else {
			refInfeasible++
			require.Equal(t, PairInfeasible, pr.Status, "pair %+v", pr.Pair)
		}
	}
	require.Equal(t, refFeasible, feasible)
	require.Equal(t, refInfeasible, infeasible)

	// Every generated case passes the ordinary parameter validation rules
	// and satisfies the constraint.
	for _, c := range report.Cases {
		input, err := c.BuildRequest(route)
		require.NoError(t, err)
		for _, param := range parametersOf(route) {
			require.NoError(t, openapi3filter.ValidateParameter(context.Background(), input, param),
				"case %v parameter %s", c.Values, param.Name)
		}
		require.True(t, constraint(c.Values))
	}
}

func TestPlannerBudgetExhausted(t *testing.T) {
	route, constraint := testRoute()
	domains := testDomains(t, route)

	report, err := (&Planner{
		Route:      route,
		Domains:    domains,
		Constraint: constraint,
		Budget:     2,
	}).Plan(context.Background())
	require.NoError(t, err)
	require.True(t, report.Exhausted)
	require.Equal(t, 2, report.Evaluations)

	// With the budget exhausted, nothing may be declared infeasible: pairs
	// without a witnessed completion are undetermined.
	_, infeasible, undetermined := report.Counts()
	require.Equal(t, 0, infeasible)
	require.Positive(t, undetermined)
}

func TestPlannerOrderIndependence(t *testing.T) {
	route, constraint := testRoute()
	domains := testDomains(t, route)

	base, err := (&Planner{Route: route, Domains: domains, Constraint: constraint}).Plan(context.Background())
	require.NoError(t, err)

	// Reverse the domain order and every candidate order: the stable set of
	// cases must not change.
	reversed := make([]Domain, 0, len(domains))
	for i := len(domains) - 1; i >= 0; i-- {
		candidates := domains[i].Candidates
		flipped := make([]Candidate, 0, len(candidates))
		for j := len(candidates) - 1; j >= 0; j-- {
			flipped = append(flipped, candidates[j])
		}
		reversed = append(reversed, Domain{Param: domains[i].Param, Candidates: flipped})
	}
	other, err := (&Planner{Route: route, Domains: reversed, Constraint: constraint}).Plan(context.Background())
	require.NoError(t, err)

	names := []string{"flag", "level", "mode"}
	require.Equal(t, len(base.Cases), len(other.Cases))
	for i := range base.Cases {
		require.Equal(t, base.Cases[i].key(names), other.Cases[i].key(names))
	}
	require.Equal(t, base.Pairs, other.Pairs)
}

func TestDomainFor(t *testing.T) {
	t.Run("enum extraction", func(t *testing.T) {
		param := openapi3.NewQueryParameter("s")
		param.Schema = openapi3.NewSchemaRef("", openapi3.NewStringSchema().WithEnum("x", "y"))
		domain, err := DomainFor(param, nil)
		require.NoError(t, err)
		require.Equal(t, []Candidate{Absent(), Value("x"), Value("y")}, domain)
	})

	t.Run("boolean extraction", func(t *testing.T) {
		param := openapi3.NewQueryParameter("b")
		param.Schema = openapi3.NewSchemaRef("", openapi3.NewBoolSchema())
		domain, err := DomainFor(param, nil)
		require.NoError(t, err)
		require.Equal(t, []Candidate{Absent(), Value(false), Value(true)}, domain)
	})

	t.Run("unbounded string rejected", func(t *testing.T) {
		param := openapi3.NewQueryParameter("s")
		param.Schema = openapi3.NewSchemaRef("", openapi3.NewStringSchema())
		_, err := DomainFor(param, nil)
		require.ErrorContains(t, err, "unbounded")
	})

	t.Run("unbounded integer rejected", func(t *testing.T) {
		param := openapi3.NewQueryParameter("n")
		param.Schema = openapi3.NewSchemaRef("", openapi3.NewIntegerSchema())
		_, err := DomainFor(param, nil)
		require.ErrorContains(t, err, "unbounded")
	})

	t.Run("explicit values allowed for unbounded types", func(t *testing.T) {
		param := openapi3.NewQueryParameter("n")
		param.Schema = openapi3.NewSchemaRef("", openapi3.NewIntegerSchema())
		domain, err := DomainFor(param, []any{7, 9})
		require.NoError(t, err)
		require.Equal(t, []Candidate{Absent(), Value(7), Value(9)}, domain)
	})

	t.Run("nullable adds null candidate", func(t *testing.T) {
		param := openapi3.NewQueryParameter("s")
		param.Schema = openapi3.NewSchemaRef("", openapi3.NewStringSchema().WithEnum("x").WithNullable())
		domain, err := DomainFor(param, nil)
		require.NoError(t, err)
		require.Equal(t, []Candidate{Absent(), Null(), Value("x")}, domain)
	})

	t.Run("required parameter is never absent", func(t *testing.T) {
		param := openapi3.NewQueryParameter("s")
		param.Required = true
		param.Schema = openapi3.NewSchemaRef("", openapi3.NewStringSchema().WithEnum("x"))
		domain, err := DomainFor(param, nil)
		require.NoError(t, err)
		require.Equal(t, []Candidate{Value("x")}, domain)
	})

	t.Run("non scalar explicit value rejected", func(t *testing.T) {
		param := openapi3.NewQueryParameter("o")
		param.Schema = openapi3.NewSchemaRef("", openapi3.NewObjectSchema())
		_, err := DomainFor(param, []any{map[string]any{"a": 1}})
		require.ErrorContains(t, err, "not a scalar")
	})
}
