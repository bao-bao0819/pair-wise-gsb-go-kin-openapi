package openapi3filter

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestEncodeParameter_EmptyContainers fixes the encoding of empty arrays and
// objects per style/location.
func TestEncodeParameter_EmptyContainers(t *testing.T) {
	t.Run("path", func(t *testing.T) {
		cases := []struct {
			style   string
			explode bool
			want    string
		}{
			{"simple", false, ""},
			{"simple", true, ""},
			{"label", false, "."},
			{"label", true, "."},
			{"matrix", false, ";p="},
			{"matrix", true, ";p="},
		}
		values := []any{[]string{}, map[string]string{}}
		for _, kind := range values {
			for _, tc := range cases {
				param := encParam(openapi3.ParameterInPath, "p", tc.style, boolPtr(tc.explode))
				enc, err := EncodeParameter(param, kind)
				require.NoError(t, err)
				require.Equal(t, tc.want, enc.PathSegment, "%s explode=%v %T", tc.style, tc.explode, kind)
			}
		}
	})

	t.Run("query array", func(t *testing.T) {
		for _, tc := range []struct {
			style string
		}{
			{"form"}, {"spaceDelimited"}, {"pipeDelimited"},
		} {
			for _, explode := range []bool{false, true} {
				param := encParam(openapi3.ParameterInQuery, "p", tc.style, boolPtr(explode))
				enc, err := EncodeParameter(param, []string{})
				require.NoError(t, err)
				require.Equal(t, []QueryPair{{"p", ""}}, enc.QueryPairs)
			}
		}
	})

	t.Run("query object form non-explode", func(t *testing.T) {
		param := encParam(openapi3.ParameterInQuery, "p", openapi3.SerializationForm, boolPtr(false))
		enc, err := EncodeParameter(param, map[string]string{})
		require.NoError(t, err)
		require.Equal(t, []QueryPair{{"p", ""}}, enc.QueryPairs)
	})

	t.Run("header", func(t *testing.T) {
		for _, explode := range []bool{false, true} {
			for _, value := range []any{[]string{}, map[string]string{}} {
				param := encParam(openapi3.ParameterInHeader, "X-P", openapi3.SerializationSimple, boolPtr(explode))
				enc, err := EncodeParameter(param, value)
				require.NoError(t, err)
				require.Equal(t, "", enc.HeaderValue)
			}
		}
	})

	t.Run("cookie", func(t *testing.T) {
		for _, value := range []any{[]string{}, map[string]string{}} {
			param := encParam(openapi3.ParameterInCookie, "p", openapi3.SerializationForm, boolPtr(false))
			enc, err := EncodeParameter(param, value)
			require.NoError(t, err)
			require.Equal(t, "", enc.HeaderValue)
		}
	})
}

// TestEncodeParameter_UnrepresentableEmptyObjects verifies styles for which an
// empty object cannot be written without losing information.
func TestEncodeParameter_UnrepresentableEmptyObjects(t *testing.T) {
	// form explode=true emits no pair at all, indistinguishable from omission.
	param := encParam(openapi3.ParameterInQuery, "p", openapi3.SerializationForm, boolPtr(true))
	_, err := EncodeParameter(param, map[string]string{})
	require.Error(t, err)
	require.Contains(t, err.Error(), `parameter "p" in query`)

	// deepObject cannot describe an empty object either.
	param = encParam(openapi3.ParameterInQuery, "p", openapi3.SerializationDeepObject, boolPtr(true))
	_, err = EncodeParameter(param, map[string]string{})
	require.Error(t, err)
}

// TestEncodeParameter_UnsupportedCombinations verifies every style/explode
// combination outside the OpenAPI matrix is rejected before any write.
func TestEncodeParameter_UnsupportedCombinations(t *testing.T) {
	cases := []struct {
		in, style string
		explode   bool
		value     any
	}{
		{"path", "form", false, "x"},
		{"path", "spaceDelimited", false, []string{"a"}},
		{"path", "deepObject", true, map[string]string{"k": "v"}},
		{"query", "simple", false, "x"},
		{"query", "label", false, "x"},
		{"query", "matrix", false, "x"},
		{"query", "deepObject", false, map[string]string{"k": "v"}},
		{"header", "form", false, "x"},
		{"header", "label", false, []string{"a"}},
		{"header", "matrix", false, []string{"a"}},
		{"cookie", "simple", false, "x"},
		{"cookie", "form", true, []string{"a", "b"}},
		{"cookie", "form", true, map[string]string{"k": "v"}},
		{"cookie", "spaceDelimited", false, []string{"a"}},
	}
	for _, tc := range cases {
		param := encParam(tc.in, "p", tc.style, boolPtr(tc.explode))
		enc, err := EncodeParameter(param, tc.value)
		require.Error(t, err, "%s/%s explode=%v", tc.in, tc.style, tc.explode)
		require.Nil(t, enc)
		require.IsType(t, &ParseError{}, err)

		// The request must not be mutated on error.
		req := newRequest(t, "http://example.com/seg/{p}?existing=1")
		err = EncodeRequestParameter(req, param, tc.value)
		require.Error(t, err)
		require.Equal(t, "/seg/{p}", req.URL.Path)
		require.Equal(t, "existing=1", req.URL.RawQuery)
	}
}

// TestEncodeParameter_AmbiguousValues verifies that a value containing a
// structural delimiter is rejected with a specific parameter error.
func TestEncodeParameter_AmbiguousValues(t *testing.T) {
	cases := []struct {
		in, style string
		explode   bool
		value     any
		field     string
	}{
		{"path", "simple", false, []string{"a,b"}, ","},
		{"path", "label", false, []string{"a,b"}, ","},
		{"path", "label", true, []string{"a.b"}, "."},
		{"path", "matrix", false, []string{"a,b"}, ","},
		{"path", "matrix", true, []string{"a;p=b"}, ";p="},
		{"path", "simple", false, map[string]string{"k": "a,b"}, ","},
		{"path", "simple", true, map[string]string{"k": "a,b"}, ","},
		{"path", "label", true, map[string]string{"k": "a.b"}, "."},
		{"path", "matrix", true, map[string]string{"k": "a;b"}, ";"},
		{"query", "form", false, []string{"a,b"}, ","},
		{"query", "spaceDelimited", false, []string{"a b"}, " "},
		{"query", "pipeDelimited", false, []string{"a|b"}, "|"},
		{"query", "form", false, map[string]string{"k": "a,b"}, ","},
		{"header", "simple", false, []string{"a,b"}, ","},
		{"header", "simple", true, []string{"a,b"}, ","},
		{"header", "simple", false, map[string]string{"k": "a,b"}, ","},
		{"header", "simple", true, map[string]string{"k": "a,b"}, ","},
		{"cookie", "form", false, []string{"a,b"}, ","},
		{"cookie", "form", false, map[string]string{"k": "a,b"}, ","},
	}
	for _, tc := range cases {
		param := encParam(tc.in, "p", tc.style, boolPtr(tc.explode))
		_, err := EncodeParameter(param, tc.value)
		require.Error(t, err, "%s/%s explode=%v", tc.in, tc.style, tc.explode)
		require.Contains(t, err.Error(), `parameter "p" in `+tc.in)
		require.Contains(t, err.Error(), tc.field)
	}
}

// TestEncodeParameter_DeepObjectNestingRejected verifies deepObject only
// supports the spec's flat objects.
func TestEncodeParameter_DeepObjectNestingRejected(t *testing.T) {
	param := encParam(openapi3.ParameterInQuery, "p", openapi3.SerializationDeepObject, boolPtr(true))

	// Nested objects are rejected through normalization for typed values.
	_, err := EncodeParameter(param, map[string]any{"outer": map[string]string{"inner": "v"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), `parameter "p" in query`)

	// Nested arrays too.
	_, err = EncodeParameter(param, map[string]any{"outer": []string{"a"}})
	require.Error(t, err)

	// deepObject never applies to arrays.
	_, err = EncodeParameter(param, []string{"a", "b"})
	require.Error(t, err)

	// A bracket-bearing key would encode as p[a[b]] which denotes nesting.
	_, err = EncodeParameter(param, map[string]string{"a[b]": "v"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "brackets")

	// spaceDelimited/pipeDelimited only apply to arrays.
	_, err = EncodeParameter(
		encParam(openapi3.ParameterInQuery, "p", openapi3.SerializationSpaceDelimited, boolPtr(false)),
		"primitive")
	require.Error(t, err)
	_, err = EncodeParameter(
		encParam(openapi3.ParameterInQuery, "p", openapi3.SerializationSpaceDelimited, boolPtr(false)),
		map[string]string{"k": "v"})
	require.Error(t, err)
	_, err = EncodeParameter(
		encParam(openapi3.ParameterInQuery, "p", openapi3.SerializationPipeDelimited, boolPtr(false)),
		map[string]string{"k": "v"})
	require.Error(t, err)
}

// TestEncodeParameter_InvalidValues verifies non-encodable Go values.
func TestEncodeParameter_InvalidValues(t *testing.T) {
	param := openapi3.NewQueryParameter("p")

	// Nested slice elements.
	_, err := EncodeParameter(param, [][]string{{"a"}})
	require.Error(t, err)

	// Non-string map keys.
	_, err = EncodeParameter(param, map[int]string{1: "v"})
	require.Error(t, err)

	// Unsupported kind (struct/channel).
	_, err = EncodeParameter(param, struct{ A string }{A: "x"})
	require.Error(t, err)

	// Nil.
	_, err = EncodeParameter(param, nil)
	require.Error(t, err)

	// Non-finite floats.
	_, err = EncodeParameter(param, math.NaN())
	require.Error(t, err)

	// Nil parameter.
	_, err = EncodeParameter(nil, "x")
	require.Error(t, err)
}

// TestEncodeParameter_InvalidBytes verifies header/cookie byte restrictions.
func TestEncodeParameter_InvalidBytes(t *testing.T) {
	// Newlines in a header would allow response/request splitting.
	header := openapi3.NewHeaderParameter("X-P")
	_, err := EncodeParameter(header, "good\r\nEvil: 1")
	require.Error(t, err)

	// Non-ASCII and dropped bytes are not silently mangled in a cookie.
	cookie := openapi3.NewCookieParameter("p")
	cookie.Explode = boolPtr(false)
	for _, v := range []string{"héllo", "a;b", `q"q`, `a\b`, "tab\there"} {
		_, err := EncodeParameter(cookie, v)
		require.Error(t, err, "cookie %q", v)
	}
}

// TestEncodeParameter_ContentParameterRejected verifies content-defined
// parameters are explicitly out of scope.
func TestEncodeParameter_ContentParameterRejected(t *testing.T) {
	param := openapi3.NewQueryParameter("p")
	param.Content = openapi3.Content{
		"application/json": &openapi3.MediaType{},
	}
	_, err := EncodeParameter(param, "x")
	require.Error(t, err)
	require.IsType(t, &ParseError{}, err)
}
