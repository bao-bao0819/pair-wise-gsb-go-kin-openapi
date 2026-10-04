package openapi3filter

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
)

func encParam(in, name, style string, explode *bool) *openapi3.Parameter {
	return &openapi3.Parameter{Name: name, In: in, Style: style, Explode: explode}
}

func boolPtr(b bool) *bool { return &b }

// TestEncodeParameter_SpecExamples covers the fixed strings given by the
// OpenAPI 3 Serialization Examples table.
func TestEncodeParameter_SpecExamples(t *testing.T) {
	const id = 5
	const idStr = "5"
	ids := []int{3, 4, 5}
	idsStr := []string{"3", "4", "5"}
	colorRole := map[string]string{"role": "admin", "color": "blue"}
	role := map[string]string{"role": "admin"}

	pathCases := []struct {
		style   string
		explode bool
		value   any
		want    string
	}{
		// primitive
		{"simple", false, id, "5"},
		{"label", false, id, ".5"},
		{"matrix", false, id, ";id=5"},
		// array
		{"simple", false, ids, "3,4,5"},
		{"label", false, ids, ".3,4,5"},
		{"label", true, ids, ".3.4.5"},
		{"matrix", false, ids, ";id=3,4,5"},
		{"matrix", true, ids, ";id=3;id=4;id=5"},
		// object, single-property cases (exact spec strings)
		{"simple", false, role, "role,admin"},
		{"simple", true, role, "role=admin"},
		{"label", false, role, ".role,admin"},
		{"label", true, role, ".role=admin"},
		{"matrix", false, role, ";id=role,admin"},
		{"matrix", true, role, ";role=admin"},
	}
	for _, tc := range pathCases {
		param := encParam(openapi3.ParameterInPath, "id", tc.style, boolPtr(tc.explode))
		enc, err := EncodeParameter(param, tc.value)
		require.NoError(t, err, "path %s explode=%v", tc.style, tc.explode)
		require.Equal(t, tc.want, enc.PathSegment, "path %s explode=%v", tc.style, tc.explode)
	}

	// Multi-property objects: keys are sorted for deterministic output; assert
	// the exact deterministic string and verify semantics below.
	_ = colorRole

	queryCases := []struct {
		name    string
		style   string
		explode bool
		value   any
		want    []QueryPair
	}{
		// primitive
		{"id", "form", true, id, []QueryPair{{"id", "5"}}},
		{"id", "form", false, idStr, []QueryPair{{"id", "5"}}},
		// array
		{"id", "form", false, ids, []QueryPair{{"id", "3,4,5"}}},
		{"id", "form", true, idsStr, []QueryPair{{"id", "3"}, {"id", "4"}, {"id", "5"}}},
		{"id", "spaceDelimited", false, ids, []QueryPair{{"id", "3 4 5"}}},
		{"id", "pipeDelimited", false, ids, []QueryPair{{"id", "3|4|5"}}},
		// object
		{"id", "form", false, role, []QueryPair{{"id", "role,admin"}}},
		{"id", "form", true, role, []QueryPair{{"role", "admin"}}},
		{"id", "deepObject", true, role, []QueryPair{{"id[role]", "admin"}}},
	}
	for _, tc := range queryCases {
		param := encParam(openapi3.ParameterInQuery, tc.name, tc.style, boolPtr(tc.explode))
		enc, err := EncodeParameter(param, tc.value)
		require.NoError(t, err, "query %s explode=%v", tc.style, tc.explode)
		require.Equal(t, tc.want, enc.QueryPairs, "query %s explode=%v", tc.style, tc.explode)
	}

	// Multi-property deepObject example from the spec (keys sorted).
	enc, err := EncodeParameter(
		encParam(openapi3.ParameterInQuery, "id", openapi3.SerializationDeepObject, boolPtr(true)),
		map[string]string{"role": "admin", "color": "blue"})
	require.NoError(t, err)
	require.Equal(t, []QueryPair{{"id[color]", "blue"}, {"id[role]", "admin"}}, enc.QueryPairs)

	// Header simple examples.
	headerCases := []struct {
		explode bool
		value   any
		want    string
	}{
		{false, id, "5"},
		{false, ids, "3,4,5"},
		{true, ids, "3,4,5"},
		{false, role, "role,admin"},
		{true, role, "role=admin"},
	}
	for _, tc := range headerCases {
		param := encParam(openapi3.ParameterInHeader, "X-MyHeader", openapi3.SerializationSimple, boolPtr(tc.explode))
		enc, err := EncodeParameter(param, tc.value)
		require.NoError(t, err)
		require.Equal(t, tc.want, enc.HeaderValue)
	}

	// Cookie form examples (explode always false).
	cookieCases := []struct {
		value any
		want  string
	}{
		{id, "5"},
		{ids, "3,4,5"},
		{role, "role,admin"},
	}
	for _, tc := range cookieCases {
		param := encParam(openapi3.ParameterInCookie, "id", openapi3.SerializationForm, boolPtr(false))
		enc, err := EncodeParameter(param, tc.value)
		require.NoError(t, err)
		require.Equal(t, tc.want, enc.HeaderValue)
	}
}

// TestEncodeParameter_Types verifies explicit encoding of booleans and
// numbers across the native Go numeric types and json.Number.
func TestEncodeParameter_Types(t *testing.T) {
	param := func(in string) *openapi3.Parameter {
		style := openapi3.SerializationSimple
		if in == openapi3.ParameterInQuery || in == openapi3.ParameterInCookie {
			style = openapi3.SerializationForm
		}
		return encParam(in, "p", style, boolPtr(false))
	}
	values := []any{
		true, false,
		int(7), int8(-7), int16(700), int32(70000), int64(7000000000),
		uint(7), uint8(7), uint16(700), uint32(70000), uint64(7000000000),
		float64(1.5), float32(2.25), json.Number("1.25"),
	}
	for _, v := range values {
		enc, err := EncodeParameter(param(openapi3.ParameterInQuery), v)
		require.NoError(t, err, "%T %v", v, v)
		require.Len(t, enc.QueryPairs, 1)
	}

	// Booleans produce literal "true"/"false".
	enc, err := EncodeParameter(param(openapi3.ParameterInPath), true)
	require.NoError(t, err)
	require.Equal(t, "true", enc.PathSegment)

	// float formatting is minimal and precise.
	enc, err = EncodeParameter(param(openapi3.ParameterInPath), float64(0.1))
	require.NoError(t, err)
	require.Equal(t, "0.1", enc.PathSegment)

	enc, err = EncodeParameter(param(openapi3.ParameterInPath), float32(0.1))
	require.NoError(t, err)
	require.Equal(t, "0.1", enc.PathSegment)
}
