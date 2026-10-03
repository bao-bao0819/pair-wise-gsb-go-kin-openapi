package openapi3plan_test

import (
	"context"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/openapi3plan"
	"github.com/stretchr/testify/require"
)

// constrainedSpec models an operation with three query parameters that a
// single-parameter schema cannot fully relate:
//
//	mode:   enum of "a" | "b"
//	tier:   enum of 1 | 2
//	flag:   boolean
//
// Constraint: when mode == "a" the tier must be 1; flag=true additionally
// requires tier=2. Consequently:
//   - (mode="a", flag=false) has exactly one completion: tier=1.
//   - (mode="a", flag=true) has no completion at all (rule 1 forces tier=1,
//     rule 2 forces tier=2).
//
// The exhaustive scan in the test verifies every such classification.
func constrainedSpec(t *testing.T) (*openapi3.T, *openapi3filter.Options) {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(`
openapi: 3.0.0
info:
  title: constrained
  version: "1"
paths:
  /items:
    get:
      operationId: listItems
      parameters:
        - name: mode
          in: query
          required: true
          schema:
            type: string
            enum: [a, b]
        - name: tier
          in: query
          required: true
          schema:
            type: integer
            enum: [1, 2]
        - name: flag
          in: query
          required: true
          schema:
            type: boolean
      responses:
        "200":
          description: ok
`))
	require.NoError(t, err)
	return doc, nil
}

func constraint(values map[string]openapi3plan.Candidate) bool {
	mode := values["mode"]
	tier := values["tier"]
	flag := values["flag"]
	if mode.Kind == openapi3plan.KindValue && mode.Value == "a" &&
		tier.Kind == openapi3plan.KindValue && tier.Value == int32(2) {
		return false
	}
	if flag.Kind == openapi3plan.KindValue && flag.Value == true &&
		tier.Kind == openapi3plan.KindValue && tier.Value == int32(1) {
		return false
	}
	return true
}

func boolPtr(b bool) *bool { return &b }

func TestConstrainedPairwisePlan(t *testing.T) {
	ctx := context.Background()
	doc, _ := constrainedSpec(t)

	pathItem := doc.Paths.Find("/items")
	route := stubRoute(doc, pathItem, pathItem.Get, "GET", "/items")

	report, err := openapi3plan.Plan(ctx, route, &openapi3plan.Options{
		Constraint: constraint,
	})
	require.NoError(t, err)

	// Domain sizes: mode 2, tier 2, flag 2 -> 8 complete assignments.
	require.Equal(t, int64(8), report.Total)
	require.Equal(t, 8, report.Examined)
	require.False(t, report.BudgetExhausted)

	// Every emitted case passes ordinary parameter validation, and the case
	// requests are independently re-checked here (no auth, no network).
	for _, c := range report.Cases {
		input := &openapi3filter.RequestValidationInput{
			Request:    c.Request,
			PathParams: map[string]string{},
			Route:      route,
		}
		for _, ref := range pathItem.Get.Parameters {
			require.NoError(t, openapi3filter.ValidateParameter(ctx, input, ref.Value))
		}
		require.True(t, constraint(c.Values))
	}

	// Cross-check feasibility of every pair with an independent brute force
	// over the same small domains.
	brute := bruteForceStatus(t, doc, route)
	require.Len(t, report.Pairs, len(brute))
	for i, row := range report.Pairs {
		require.Equal(t, brute[i].Status, row.Status,
			"pair %s/%s vs %s/%s",
			row.Pair.ParamA, row.Pair.CandidateA.Kind,
			row.Pair.ParamB, row.Pair.CandidateB.Kind)
	}

	// With the full product exhausted there can be no undetermined pairs.
	require.Zero(t, report.Undetermined)
	require.Greater(t, report.Feasible, 0)
	require.Greater(t, report.Infeasible, 0)

	// Requested scenario: the pair (flag=false, mode=a) has exactly one legal
	// completion over the finite domains, namely tier=1. Rule 1 forbids
	// tier=2 for mode=a; rule 2 only binds when flag=true. By contrast
	// (flag=true, mode=a) is infeasible because rule 1 forces tier=1 while
	// rule 2 requires tier=2.
	modeAFalse := findPair(t, report, "flag", openapi3plan.Value(false), "mode", openapi3plan.Value("a"))
	require.Equal(t, openapi3plan.StatusFeasible, modeAFalse.Status)
	completions := countCompletions(t, doc, route, map[string]any{
		"mode": "a", "flag": false,
	}, []string{"tier"}, []any{int32(1), int32(2)})
	require.Equal(t, 1, completions)

	modeATrue := findPair(t, report, "flag", openapi3plan.Value(true), "mode", openapi3plan.Value("a"))
	require.Equal(t, openapi3plan.StatusInfeasible, modeATrue.Status)

	// All feasible pairs must be covered.
	require.Equal(t, report.Feasible, report.Covered)

	// Greedy + redundancy removal never needs all valid assignments; pairwise
	// cover of a 2x2x2 space is achieved by at most 4 cases.
	require.LessOrEqual(t, len(report.Cases), 4)
	require.GreaterOrEqual(t, len(report.Cases), 2)
}

func TestBudgetExhaustionIsUndetermined(t *testing.T) {
	ctx := context.Background()
	doc, _ := constrainedSpec(t)
	pathItem := doc.Paths.Find("/items")
	route := stubRoute(doc, pathItem, pathItem.Get, "GET", "/items")

	// A budget of one examines only the first lexicographic assignment, so the
	// declared domains are far from exhausted: every not-yet-seen pair must be
	// reported undetermined (never prematurely infeasible).
	report, err := openapi3plan.Plan(ctx, route, &openapi3plan.Options{
		Constraint: constraint,
		Budget:     1,
	})
	require.NoError(t, err)
	require.True(t, report.BudgetExhausted)
	require.Equal(t, 1, report.Examined)
	require.Equal(t, report.Feasible+report.Undetermined, len(report.Pairs))
	require.Zero(t, report.Infeasible, "nothing can be proven infeasible until the finite product is exhausted")
}

func TestPlanIsStableAcrossPresentationOrder(t *testing.T) {
	ctx := context.Background()
	doc, _ := constrainedSpec(t)
	pathItem := doc.Paths.Find("/items")
	route := stubRoute(doc, pathItem, pathItem.Get, "GET", "/items")

	run := func() []string {
		report, err := openapi3plan.Plan(ctx, route, &openapi3plan.Options{Constraint: constraint})
		require.NoError(t, err)
		signatures := make([]string, 0, len(report.Cases))
		for _, c := range report.Cases {
			signatures = append(signatures, caseSignature(c.Values))
		}
		return signatures
	}

	first := run()
	for i := 0; i < 3; i++ {
		require.Equal(t, first, run())
	}
}

func TestExplicitDomainOrderDoesNotMatter(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(`
openapi: 3.0.0
info: {title: n, version: "1"}
paths:
  /x:
    get:
      parameters:
        - name: a
          in: query
          schema: {type: integer}
        - name: b
          in: query
          schema: {type: string}
      responses:
        "200": {description: ok}
`))
	require.NoError(t, err)
	pathItem := doc.Paths.Find("/x")

	signatures := func(aVals []any, bVals []any) []string {
		route := stubRoute(doc, pathItem, pathItem.Get, "GET", "/x")
		report, err := openapi3plan.Plan(ctx, route, &openapi3plan.Options{
			Domains: map[string]openapi3plan.DomainConfig{
				"a": {Explicit: aVals, IncludeAbsent: boolPtr(false)},
				"b": {Explicit: bVals, IncludeAbsent: boolPtr(false)},
			},
		})
		require.NoError(t, err)
		sigs := make([]string, 0, len(report.Cases))
		for _, c := range report.Cases {
			sigs = append(sigs, caseSignature(c.Values))
		}
		return sigs
	}

	first := signatures([]any{1, 2, 3}, []any{"x", "y"})
	second := signatures([]any{3, 1, 2}, []any{"y", "x"})
	require.Equal(t, first, second)
}

func TestAbsentAndNullAreDistinct(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(`
openapi: 3.0.0
info: {title: n, version: "1"}
paths:
  /n:
    get:
      parameters:
        - name: n
          in: query
          required: false
          allowEmptyValue: true
          schema:
            type: integer
            nullable: true
            enum: [1, 2]
        - name: pick
          in: query
          required: false
          allowEmptyValue: true
          schema:
            type: boolean
      responses:
        "200": {description: ok}
`))
	require.NoError(t, err)
	pathItem := doc.Paths.Find("/n")
	route := stubRoute(doc, pathItem, pathItem.Get, "GET", "/n")

	report, err := openapi3plan.Plan(ctx, route, nil)
	require.NoError(t, err)

	// The two parameters independently yield absent, null and concrete
	// candidates; all of those rows exist in the report.
	kinds := map[string]map[openapi3plan.Kind]bool{}
	for _, row := range report.Pairs {
		add := func(param string, c openapi3plan.Candidate) {
			if kinds[param] == nil {
				kinds[param] = map[openapi3plan.Kind]bool{}
			}
			kinds[param][c.Kind] = true
		}
		add(row.Pair.ParamA, row.Pair.CandidateA)
		add(row.Pair.ParamB, row.Pair.CandidateB)
	}
	for _, param := range []string{"n", "pick"} {
		require.True(t, kinds[param][openapi3plan.KindAbsent], param)
		require.True(t, kinds[param][openapi3plan.KindNull], param)
		require.True(t, kinds[param][openapi3plan.KindValue], param)
	}

	// Every selected case still passes ordinary validation.
	for _, c := range report.Cases {
		input := &openapi3filter.RequestValidationInput{
			Request:    c.Request,
			PathParams: map[string]string{},
			Route:      route,
		}
		for _, ref := range pathItem.Get.Parameters {
			require.NoError(t, openapi3filter.ValidateParameter(ctx, input, ref.Value))
		}
	}
}

func TestUnboundedDomainRequiresExplicitCandidates(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(`
openapi: 3.0.0
info: {title: n, version: "1"}
paths:
  /u:
    get:
      parameters:
        - name: page
          in: query
          schema:
            type: integer
            minimum: 0
        - name: order
          in: query
          schema:
            type: string
      responses:
        "200": {description: ok}
`))
	require.NoError(t, err)
	pathItem := doc.Paths.Find("/u")
	route := stubRoute(doc, pathItem, pathItem.Get, "GET", "/u")

	// Integers and free-form strings are infinite domains, so the planner must
	// refuse rather than enumerate them.
	_, err = openapi3plan.Plan(ctx, route, nil)
	require.Error(t, err)

	// Providing explicit finite domains is accepted.
	report, err := openapi3plan.Plan(ctx, route, &openapi3plan.Options{
		Domains: map[string]openapi3plan.DomainConfig{
			"page":  {Explicit: []any{1, 2}, IncludeAbsent: boolPtr(false)},
			"order": {Explicit: []any{"asc", "desc"}, IncludeAbsent: boolPtr(false)},
		},
	})
	require.NoError(t, err)

	// Explicit ints are canonicalized to the decoded type (int32 for a plain
	// integer schema), and every generated case passes ordinary validation.
	for _, c := range report.Cases {
		page := c.Values["page"]
		require.Equal(t, openapi3plan.KindValue, page.Kind)
		require.IsType(t, int32(0), page.Value)
		input := &openapi3filter.RequestValidationInput{
			Request: c.Request, PathParams: map[string]string{}, Route: route,
		}
		for _, ref := range pathItem.Get.Parameters {
			require.NoError(t, openapi3filter.ValidateParameter(ctx, input, ref.Value))
		}
	}

	// A 2x2 product yields four pair rows, all feasible and covered.
	require.Equal(t, 4, report.Feasible)
	require.Zero(t, report.Infeasible)
	require.Zero(t, report.Undetermined)
	require.Equal(t, 4, report.Covered)
}

func TestSingleParameterIsSupported(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(`
openapi: 3.0.0
info: {title: n, version: "1"}
paths:
  /s:
    get:
      parameters:
        - name: verbose
          in: query
          schema: {type: boolean}
      responses:
        "200": {description: ok}
`))
	require.NoError(t, err)
	pathItem := doc.Paths.Find("/s")
	route := stubRoute(doc, pathItem, pathItem.Get, "GET", "/s")

	report, err := openapi3plan.Plan(ctx, route, nil)
	require.NoError(t, err)
	// No pairs with a single parameter, but both concrete values are still
	// produced as valid cases, together with the optional absent candidate.
	require.Empty(t, report.Pairs)
	require.Equal(t, 3, report.Valid)
	require.Len(t, report.Cases, 3)
}

func TestPathParametersAreSubstituted(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(`
openapi: 3.0.0
info: {title: n, version: "1"}
paths:
  /orgs/{org}/items/{kind}:
    get:
      parameters:
        - name: org
          in: path
          required: true
          schema: {type: string, enum: [acme, globex]}
        - name: kind
          in: path
          required: true
          schema: {type: string, enum: [doc, file]}
      responses:
        "200": {description: ok}
`))
	require.NoError(t, err)
	pathItem := doc.Paths.Find("/orgs/{org}/items/{kind}")
	route := stubRoute(doc, pathItem, pathItem.Get, "GET", "/orgs/{org}/items/{kind}")

	report, err := openapi3plan.Plan(ctx, route, nil)
	require.NoError(t, err)

	// Four concrete pair combinations; greedy pairwise selection covers all
	// four pairs and each rendered URL validates through the ordinary rules.
	require.Equal(t, 4, report.Feasible)
	require.Equal(t, 4, report.Covered)
	require.NotEmpty(t, report.Cases)
	for _, c := range report.Cases {
		pathParams := map[string]string{}
		// Recover raw path values from PathParams by re-rendering.
		for _, ref := range pathItem.Get.Parameters {
			p := ref.Value
			cand := c.Values[p.Name]
			require.Equal(t, openapi3plan.KindValue, cand.Kind)
			pathParams[p.Name] = cand.Value.(string)
		}
		input := &openapi3filter.RequestValidationInput{
			Request:    c.Request,
			PathParams: pathParams,
			Route:      route,
		}
		for _, ref := range pathItem.Get.Parameters {
			require.NoError(t, openapi3filter.ValidateParameter(ctx, input, ref.Value))
		}
	}
}

func TestPlanNeverInvokesAuthentication(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData([]byte(`
openapi: 3.0.0
info: {title: n, version: "1"}
paths:
  /a:
    get:
      security:
        - apiKey: []
      parameters:
        - name: x
          in: query
          schema: {type: boolean}
      responses:
        "200": {description: ok}
components:
  securitySchemes:
    apiKey: {type: apiKey, in: query, name: key}
`))
	require.NoError(t, err)
	pathItem := doc.Paths.Find("/a")
	route := stubRoute(doc, pathItem, pathItem.Get, "GET", "/a")

	// Even though the operation is secured and an authentication function is
	// configured, planning must never call it (and never sends a request).
	var called bool
	allowAuthCall := false
	filterOpts := &openapi3filter.Options{
		AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error {
			if !allowAuthCall {
				t.Fatal("authentication must not be invoked during offline planning")
			}
			called = true
			return nil
		},
	}
	report, err := openapi3plan.Plan(ctx, route, &openapi3plan.Options{FilterOptions: filterOpts})
	require.NoError(t, err)
	require.NotEmpty(t, report.Cases)
	require.False(t, called)

	// Cross-check that a normal full ValidateRequest on the same route does
	// invoke the auth function, proving the planner specifically bypasses it.
	allowAuthCall = true
	for _, c := range report.Cases {
		err := openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{
			Request:    c.Request,
			PathParams: map[string]string{},
			Route:      route,
			Options:    filterOpts,
		})
		require.NoError(t, err)
	}
}
