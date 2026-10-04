package openapi3filter

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestEncodeParameter_UnsupportedCombinations verifies the allowed matrix of
// the OpenAPI parameter serialization table: unsupported combinations fail
// before anything is written to a URL or header.
func TestEncodeParameter_UnsupportedCombinations(t *testing.T) {
	cases := []struct {
		name  string
		param *openapi3.Parameter
		value any
	}{
		{"path form style", param("path", "p", "form", boolPtr(true)), "v"},
		{"query label style", param("query", "p", "label", boolPtr(false)), "v"},
		{"header form style", param("header", "X-P", "form", boolPtr(false)), "v"},
		{"cookie simple style", param("cookie", "c", "simple", boolPtr(false)), "v"},
		{"header matrix style", param("header", "X-P", "matrix", boolPtr(false)), "v"},
		{"query spaceDelimited object", param("query", "p", "spaceDelimited", boolPtr(false)), map[string]any{"a": "b"}},
		{"query pipeDelimited object", param("query", "p", "pipeDelimited", boolPtr(false)), map[string]any{"a": "b"}},
		{"query spaceDelimited primitive", param("query", "p", "spaceDelimited", boolPtr(false)), "v"},
		{"query deepObject non-explode", param("query", "p", "deepObject", boolPtr(false)), map[string]any{"a": "b"}},
		{"query deepObject primitive", param("query", "p", "deepObject", boolPtr(true)), "v"},
		{"query deepObject array", param("query", "p", "deepObject", boolPtr(true)), []any{"a"}},
		{"cookie explode array", param("cookie", "c", "form", boolPtr(true)), []any{"a"}},
		{"cookie explode object", param("cookie", "c", "form", boolPtr(true)), map[string]any{"a": "b"}},
		{"unknown in", &openapi3.Parameter{Name: "p", In: "mouth"}, "v"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := EncodeParameter(tc.param, tc.value)
			require.Error(t, err)
			require.Nil(t, enc)

			var pe *ParseError
			require.ErrorAs(t, err, &pe)
			require.Equal(t, KindInvalidFormat, pe.Kind)
			require.Contains(t, err.Error(), tc.param.Name)
		})
	}
}

// TestEncodeParameter_UnrepresentableValues verifies inputs that the styles
// cannot represent without information loss produce a specific parameter error
// rather than silently dropping or corrupting data.
func TestEncodeParameter_UnrepresentableValues(t *testing.T) {
	cases := []struct {
		name   string
		param  *openapi3.Parameter
		value  any
		errSub string
	}{
		{"null value", param("query", "p", "form", boolPtr(true)), nil, "null"},
		{"nested object in form object", param("query", "p", "form", boolPtr(true)),
			map[string]any{"a": map[string]any{"b": "c"}}, "object property"},
		{"nested array in form explode array", param("query", "p", "form", boolPtr(true)),
			[]any{[]any{"a"}}, "array item 0"},
		{"nested object in deepObject", param("query", "p", "deepObject", boolPtr(true)),
			map[string]any{"a": map[string]any{"b": "c"}}, "object property"},
		{"nested array in deepObject", param("query", "p", "deepObject", boolPtr(true)),
			map[string]any{"a": []any{"b"}}, "object property"},
		{"bracketed deepObject property name", param("query", "p", "deepObject", boolPtr(true)),
			map[string]any{"a[b]": "c"}, "brackets"},

		{"comma in simple array item", param("path", "p", "simple", boolPtr(false)), []any{"a,b"}, "structural"},
		{"dot in label explode item", param("path", "p", "label", boolPtr(true)), []any{"a.b"}, "structural"},
		{"semicolon in matrix explode item", param("path", "p", "matrix", boolPtr(true)), []any{"a;b"}, "structural"},
		{"equals in simple explode object value", param("path", "p", "simple", boolPtr(true)),
			orderedProps{{"a", "b=c"}}, "structural"},

		{"comma in form non-explode array", param("query", "p", "form", boolPtr(false)), []any{"a,b"}, "delimiter"},
		{"space in spaceDelimited item", param("query", "p", "spaceDelimited", boolPtr(false)), []any{"a b"}, "delimiter"},
		{"pipe in pipeDelimited item", param("query", "p", "pipeDelimited", boolPtr(false)), []any{"a|b"}, "delimiter"},
		{"comma in form non-explode object", param("query", "p", "form", boolPtr(false)),
			orderedProps{{"a", "b,c"}}, "delimiter"},

		{"comma in header array item", param("header", "X-P", "simple", boolPtr(false)), []any{"a,b"}, "structural"},
		{"comma in header object value", param("header", "X-P", "simple", boolPtr(false)),
			orderedProps{{"a", "b,c"}}, "structural"},
		{"non-ascii header value", param("header", "X-P", "simple", boolPtr(false)), "中", "HTTP header"},
		{"newline in header value", param("header", "X-P", "simple", boolPtr(false)), "a\nb", "HTTP header"},

		{"comma in cookie array item", param("cookie", "c", "form", boolPtr(false)), []any{"a,b"}, "structural"},
		{"semicolon in cookie value", param("cookie", "c", "form", boolPtr(false)), "a;b", "cookie-octet"},
		{"space in cookie value", param("cookie", "c", "form", boolPtr(false)), "a b", "cookie-octet"},
		{"non-ascii cookie value", param("cookie", "c", "form", boolPtr(false)), "中", "cookie-octet"},
		{"invalid cookie name", &openapi3.Parameter{Name: "bad name", In: "cookie", Style: "form"}, "v", "cookie name"},

		{"empty simple array", param("path", "p", "simple", boolPtr(false)), []any{}, "empty"},
		{"empty label object", param("path", "p", "label", boolPtr(false)), map[string]any{}, "empty"},
		{"empty query explode object", param("query", "p", "form", boolPtr(true)), map[string]any{}, "empty"},
		{"empty deepObject", param("query", "p", "deepObject", boolPtr(true)), map[string]any{}, "empty"},
		{"empty header array", param("header", "X-P", "simple", boolPtr(false)), []any{}, "empty"},
		{"empty cookie object", param("cookie", "c", "form", boolPtr(false)), map[string]any{}, "empty"},
		{"empty spaceDelimited array", param("query", "p", "spaceDelimited", boolPtr(false)), []any{}, "empty"},

		{"struct value", param("query", "p", "form", boolPtr(true)), struct{ X string }{"v"}, "type"},
		{"non-string map keys", param("query", "p", "form", boolPtr(true)), map[int]string{1: "v"}, "keys"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := EncodeParameter(tc.param, tc.value)
			require.Error(t, err)
			require.Nil(t, enc)
			require.Contains(t, err.Error(), tc.errSub)
		})
	}
}

// TestEncodeParameter_AllowReserved verifies allowReserved applies only to
// query values: reserved characters pass through, but structural characters
// and keys are always escaped.
func TestEncodeParameter_AllowReserved(t *testing.T) {
	t.Run("reserved chars allowed in value", func(t *testing.T) {
		p := &openapi3.Parameter{Name: "p", In: "query", AllowReserved: true}
		enc, err := EncodeParameter(p, "/?:@!$'()*+,;=[]%")
		require.NoError(t, err)
		// "&" must stay escaped to avoid a second parameter; "%" is escaped so
		// it cannot forge a percent-escape.
		require.Equal(t, "p=/?:@!$'()*+,;=[]%25", enc.Query[0])
	})

	t.Run("ampersand always escaped", func(t *testing.T) {
		p := &openapi3.Parameter{Name: "p", In: "query", AllowReserved: true}
		enc, err := EncodeParameter(p, "a&b")
		require.NoError(t, err)
		require.Equal(t, "p=a%26b", enc.Query[0])
	})

	t.Run("key is always escaped", func(t *testing.T) {
		p := &openapi3.Parameter{Name: "a&b", In: "query", Style: "form", Explode: boolPtr(true), AllowReserved: true}
		enc, err := EncodeParameter(p, map[string]any{"x": "y"})
		require.NoError(t, err)
		require.Equal(t, "x=y", enc.Query[0])
	})

	t.Run("allowReserved ignored for non-explode array delimiter", func(t *testing.T) {
		p := &openapi3.Parameter{Name: "p", In: "query", Style: "form", Explode: boolPtr(false), AllowReserved: true}
		_, err := EncodeParameter(p, []any{"a,b"})
		require.Error(t, err)
	})

	t.Run("allowReserved ignored outside query", func(t *testing.T) {
		p := param("path", "p", "simple", boolPtr(false))
		p.AllowReserved = true
		enc, err := EncodeParameter(p, "a/b")
		require.NoError(t, err)
		require.Equal(t, "a%2Fb", enc.PathSegment)
	})
}

// TestEncodedParameter_SetOnRequest verifies query order and unrelated
// duplicates are preserved, and that path/header/cookie writes land in the
// expected places.
func TestEncodedParameter_SetOnRequest(t *testing.T) {
	t.Run("query keeps unrelated order and duplicates", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://example.com/?a=1&p=old&b=2&p=again&a=3", nil)
		require.NoError(t, err)

		p := &openapi3.Parameter{Name: "p", In: "query", Style: "form", Explode: boolPtr(true)}
		enc, err := EncodeParameter(p, []any{"x", "y"})
		require.NoError(t, err)

		pathParams, err := enc.SetOnRequest(req, nil)
		require.NoError(t, err)
		require.Nil(t, pathParams)

		require.Equal(t, "a=1&b=2&a=3&p=x&p=y", req.URL.RawQuery)

		values, err := url.ParseQuery(req.URL.RawQuery)
		require.NoError(t, err)
		require.Equal(t, []string{"1", "3"}, values["a"])
		require.Equal(t, []string{"x", "y"}, values["p"])
		require.Equal(t, "2", values.Get("b"))
	})

	t.Run("escaped existing key is matched", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://example.com/?weird%5Bp%5D=old&keep=1", nil)
		require.NoError(t, err)
		p := &openapi3.Parameter{Name: "weird[p]", In: "query"}
		enc, err := EncodeParameter(p, "new")
		require.NoError(t, err)
		_, err = enc.SetOnRequest(req, nil)
		require.NoError(t, err)
		require.Equal(t, "keep=1&weird%5Bp%5D=new", req.URL.RawQuery)
	})

	t.Run("path/header/cookie", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://example.com/", nil)
		require.NoError(t, err)

		pathEnc, err := EncodeParameter(param("path", "id", "matrix", boolPtr(false)), 42)
		require.NoError(t, err)
		pp, err := pathEnc.SetOnRequest(req, nil)
		require.NoError(t, err)
		require.Equal(t, ";id=42", pp["id"])

		headerEnc, err := EncodeParameter(param("header", "X-Trace", "simple", boolPtr(false)), "abc")
		require.NoError(t, err)
		_, err = headerEnc.SetOnRequest(req, pp)
		require.NoError(t, err)
		require.Equal(t, "abc", req.Header.Get("X-Trace"))

		cookieEnc, err := EncodeParameter(param("cookie", "session", "form", boolPtr(false)), "xyz")
		require.NoError(t, err)
		_, err = cookieEnc.SetOnRequest(req, pp)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(req.Header.Get("Cookie"), "session=xyz"))
	})
}

// TestEncodeParameter_BooleansAndNumbers verifies the primitive encodings.
func TestEncodeParameter_BooleansAndNumbers(t *testing.T) {
	p := &openapi3.Parameter{Name: "p", In: "query"}
	for _, tc := range []struct {
		value any
		want  string
	}{
		{true, "p=true"},
		{false, "p=false"},
		{int(42), "p=42"},
		{int64(-7), "p=-7"},
		{uint(3), "p=3"},
		{float64(1.5), "p=1.5"},
		{float32(2.5), "p=2.5"},
		{json.Number("1e10"), "p=1e10"},
		{"", "p="},
	} {
		enc, err := EncodeParameter(p, tc.value)
		require.NoError(t, err)
		require.Equal(t, tc.want, enc.Query[0])
	}
}
