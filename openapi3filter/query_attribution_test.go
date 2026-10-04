package openapi3filter

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
)

func explodeObjectParam(name string, props map[string]*openapi3.SchemaRef) *openapi3.Parameter {
	return &openapi3.Parameter{
		Name:    name,
		In:      openapi3.ParameterInQuery,
		Style:   "form",
		Explode: explode,
		Schema:  &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: props}},
	}
}

func deepObjectParam(name string, schema *openapi3.SchemaRef) *openapi3.Parameter {
	return &openapi3.Parameter{
		Name:    name,
		In:      openapi3.ParameterInQuery,
		Style:   "deepObject",
		Explode: explode,
		Schema:  schema,
	}
}

func explodeObjectSchemaParam(name string, schema *openapi3.SchemaRef) *openapi3.Parameter {
	return &openapi3.Parameter{
		Name:    name,
		In:      openapi3.ParameterInQuery,
		Style:   "form",
		Explode: explode,
		Schema:  schema,
	}
}

func paramsRef(params ...*openapi3.Parameter) openapi3.Parameters {
	refs := make(openapi3.Parameters, 0, len(params))
	for _, p := range params {
		refs = append(refs, &openapi3.ParameterRef{Value: p})
	}
	return refs
}

// compileQueryParameterPlan rejects collisions known from the spec alone.
func TestCompileQueryParameterPlan_StaticConflicts(t *testing.T) {
	t.Run("two exploded objects share a property", func(t *testing.T) {
		first := explodeObjectParam("first", map[string]*openapi3.SchemaRef{
			"id":   stringSchema,
			"kind": stringSchema,
		})
		second := explodeObjectParam("second", map[string]*openapi3.SchemaRef{
			"id":   stringSchema,
			"name": stringSchema,
		})
		_, err := compileQueryParameterPlan(nil, paramsRef(first, second))
		var planErr *ParameterPlanError
		require.ErrorAs(t, err, &planErr)
		require.Len(t, planErr.Conflicts, 1)
		assert.Equal(t, "id", planErr.Conflicts[0].Key)
		assert.ElementsMatch(t, [2]string{"first", "second"}, planErr.Conflicts[0].Parameters)
	})

	t.Run("exploded object property collides with explicit param", func(t *testing.T) {
		id := &openapi3.Parameter{Name: "id", In: openapi3.ParameterInQuery, Schema: stringSchema}
		obj := explodeObjectParam("filter", map[string]*openapi3.SchemaRef{
			"id":   stringSchema,
			"name": stringSchema,
		})
		_, err := compileQueryParameterPlan(nil, paramsRef(id, obj))
		var planErr *ParameterPlanError
		require.ErrorAs(t, err, &planErr)
		require.Len(t, planErr.Conflicts, 1)
		assert.Equal(t, "id", planErr.Conflicts[0].Key)
	})

	t.Run("deepObject prefix collides with an explicit bracketed param name", func(t *testing.T) {
		f := deepObjectParam("f", objectOf("x", stringSchema))
		bracketed := &openapi3.Parameter{Name: "f[x]", In: openapi3.ParameterInQuery, Schema: stringSchema}
		_, err := compileQueryParameterPlan(nil, paramsRef(f, bracketed))
		var planErr *ParameterPlanError
		require.ErrorAs(t, err, &planErr)
		require.Len(t, planErr.Conflicts, 1)
		assert.Equal(t, "f[x]", planErr.Conflicts[0].Key)
	})

	t.Run("disjoint closed objects compile", func(t *testing.T) {
		first := explodeObjectParam("first", map[string]*openapi3.SchemaRef{"id": stringSchema})
		second := explodeObjectParam("second", map[string]*openapi3.SchemaRef{"name": stringSchema})
		_, err := compileQueryParameterPlan(nil, paramsRef(first, second))
		require.NoError(t, err)
	})
}

// compileQueryParameterPlan follows path item -> operation override rules.
func TestCompileQueryParameterPlan_PathItemOverride(t *testing.T) {
	pathItem := explodeObjectParam("id", map[string]*openapi3.SchemaRef{"only": stringSchema})
	override := &openapi3.Parameter{Name: "id", In: openapi3.ParameterInQuery, Schema: stringSchema}
	other := explodeObjectParam("other", map[string]*openapi3.SchemaRef{"id": stringSchema})

	// The operation overrides the path item's "id", so the path item object
	// (whose "only" property would be harmless) must not participate; but the
	// explicit "id" still collides statically with "other.id".
	_, err := compileQueryParameterPlan(paramsRef(pathItem), paramsRef(override, other))
	var planErr *ParameterPlanError
	require.ErrorAs(t, err, &planErr)
	require.Len(t, planErr.Conflicts, 1)
	assert.Equal(t, "id", planErr.Conflicts[0].Key)
}

// Every raw item is attributed to at most one parameter; legitimate array
// values keep all repetitions under one parameter.
func TestAttributeQuery_DisjointExplodedObjects(t *testing.T) {
	first := explodeObjectParam("first", map[string]*openapi3.SchemaRef{
		"id":  stringSchema,
		"tag": stringArraySchema,
	})
	second := explodeObjectParam("second", map[string]*openapi3.SchemaRef{"name": stringSchema})
	plan, err := compileQueryParameterPlan(nil, paramsRef(first, second))
	require.NoError(t, err)

	const raw = "id=1&tag=a&name=bob&tag=b"
	items := parseRawQuery(raw)
	values, report := attributeQuery(plan, items)

	require.Empty(t, report.Ambiguous)
	require.Empty(t, report.Unconsumed)

	assert.Equal(t, []string{"1"}, values["first"]["id"])
	assert.Equal(t, []string{"a", "b"}, values["first"]["tag"])
	assert.Equal(t, []string{"bob"}, values["second"]["name"])

	// Each raw key appears in exactly one parameter view.
	seen := map[string]int{}
	for _, q := range values {
		for key := range q {
			seen[key]++
		}
	}
	for key, count := range seen {
		require.Equalf(t, 1, count, "key %q attributed to %d parameters", key, count)
	}
}

// Static ownership wins over open additionalProperties, deterministically.
func TestAttributeQuery_ClosedWinsOverOpen(t *testing.T) {
	closed := explodeObjectParam("closed", map[string]*openapi3.SchemaRef{"id": stringSchema})
	openObj := explodeObjectSchemaParam("open", additionalPropertiesObjectStringSchema)
	plan, err := compileQueryParameterPlan(nil, paramsRef(closed, openObj))
	require.NoError(t, err)

	values, report := attributeQuery(plan, parseRawQuery("id=1&extra=2"))
	require.Empty(t, report.Ambiguous)
	require.Empty(t, report.Unconsumed)
	assert.Equal(t, []string{"1"}, values["closed"]["id"])
	assert.Equal(t, []string{"2"}, values["open"]["extra"])
}

// Two open exploded objects cannot be disambiguated: the raw item plus all
// candidates is reported, repeated items keep their ordinal.
func TestAttributeQuery_TwoOpenObjectsAmbiguous(t *testing.T) {
	a := explodeObjectSchemaParam("a", additionalPropertiesObjectStringSchema)
	b := explodeObjectSchemaParam("b", additionalPropertiesObjectStringSchema)
	plan, err := compileQueryParameterPlan(nil, paramsRef(a, b))
	require.NoError(t, err)

	values, report := attributeQuery(plan, parseRawQuery("shared=1&shared=3"))
	require.Len(t, report.Ambiguous, 2)
	assert.Equal(t, 0, report.Ambiguous[0].Index)
	assert.Equal(t, "shared=1", report.Ambiguous[0].Raw)
	assert.ElementsMatch(t, []string{"a", "b"}, report.Ambiguous[0].Candidates)
	assert.Equal(t, 1, report.Ambiguous[1].Index)
	assert.Equal(t, "shared=3", report.Ambiguous[1].Raw)

	_, hasA := values["a"]
	assert.False(t, hasA)
	_, hasB := values["b"]
	assert.False(t, hasB)
}

// Items no parameter can own are reported separately.
func TestAttributeQuery_Unconsumed(t *testing.T) {
	p := explodeObjectParam("p", map[string]*openapi3.SchemaRef{"id": stringSchema})
	plan, err := compileQueryParameterPlan(nil, paramsRef(p))
	require.NoError(t, err)

	_, report := attributeQuery(plan, parseRawQuery("id=1&mystery=2"))
	require.Len(t, report.Unconsumed, 1)
	assert.Equal(t, 1, report.Unconsumed[0].Index)
	assert.Equal(t, "mystery=2", report.Unconsumed[0].Raw)
}

// Escaped square brackets are decoded before prefix matching, so an encoded
// bracket cannot forge ownership by another deepObject parameter.
func TestAttributeQuery_DeepObjectEscapedBrackets(t *testing.T) {
	f := deepObjectParam("f", objectOf("g", stringSchema, "x", stringSchema))
	g := deepObjectParam("g", objectOf("h", stringSchema))
	plan, err := compileQueryParameterPlan(nil, paramsRef(f, g))
	require.NoError(t, err)

	// "f%5Bg%5D=1" decodes to key "f[g]" (a declared root prop of f). The
	// literal brackets must not be re-interpreted as nesting, and it must not
	// look like a "g[...]" item either.
	const raw = "f%5Bg%5D=1&g%5Bh%5D=2&f%5Bx%5D=3"
	values, report := attributeQuery(plan, parseRawQuery(raw))
	require.Empty(t, report.Ambiguous)
	require.Empty(t, report.Unconsumed)

	fv := values["f"]
	gv := values["g"]
	assert.Equal(t, []string{"1"}, fv["f[g]"])
	assert.Equal(t, []string{"3"}, fv["f[x]"])
	assert.Equal(t, []string{"2"}, gv["g[h]"])

	seen := map[string]int{}
	for _, q := range values {
		for key := range q {
			seen[key]++
		}
	}
	for key, count := range seen {
		require.Equalf(t, 1, count, "key %q attributed to %d parameters", key, count)
	}
}

// The per-parameter decode through strict attribution matches the historical
// loose result for a single-parameter request, proving strict mode keeps the
// valid data shape identical.
func TestStrictQueryDecode_MatchesLooseShape(t *testing.T) {
	param := explodeObjectParam("p", map[string]*openapi3.SchemaRef{
		"id":   stringSchema,
		"name": stringSchema,
	})
	plan, err := compileQueryParameterPlan(nil, paramsRef(param))
	require.NoError(t, err)

	looseReq := reqWithQuery("id=1&name=bob")
	looseValue, looseFound, looseErr := decodeStyledParameter(param, &RequestValidationInput{Request: looseReq})

	items := parseRawQuery("id=1&name=bob")
	values, _ := attributeQuery(plan, items)
	strictReq := reqWithQuery("id=1&name=bob")
	strictInput := &RequestValidationInput{Request: strictReq, strictQuery: values}
	strictValue, strictFound, strictErr := decodeStyledParameter(param, strictInput)

	require.NoError(t, looseErr)
	require.NoError(t, strictErr)
	assert.Equal(t, looseFound, strictFound)
	assert.Equal(t, looseValue, strictValue)
	assert.Equal(t, map[string]any{"id": "1", "name": "bob"}, strictValue)
}

// Loose decoding deliberately reads the whole query per parameter: two
// exploded objects that share a property both observe that same item. This is
// the historical behavior strict attribution is meant to remove, and it must
// keep working when the strict option is not enabled.
func TestLooseDecode_TwoObjectsShareItem(t *testing.T) {
	first := explodeObjectParam("first", map[string]*openapi3.SchemaRef{"id": stringSchema})
	second := explodeObjectParam("second", map[string]*openapi3.SchemaRef{"id": stringSchema})

	input := &RequestValidationInput{Request: reqWithQuery("id=1")}
	firstValue, firstFound, err := decodeStyledParameter(first, input)
	require.NoError(t, err)
	secondValue, secondFound, err := decodeStyledParameter(second, input)
	require.NoError(t, err)

	assert.True(t, firstFound)
	assert.True(t, secondFound)
	assert.Equal(t, map[string]any{"id": "1"}, firstValue)
	assert.Equal(t, map[string]any{"id": "1"}, secondValue)
}

func reqWithQuery(raw string) *http.Request {
	req, err := http.NewRequest(http.MethodGet, "http://example.test/test?"+raw, nil)
	if err != nil {
		panic(err)
	}
	return req
}
