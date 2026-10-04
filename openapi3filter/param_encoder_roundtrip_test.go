package openapi3filter

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestEncodeParameter_QueryRoundTrip encodes values containing a space, a
// slash, a percent sign, a comma and non-ASCII characters, sends them over
// HTTP, and decodes them with the same decoder used by request validation.
func TestEncodeParameter_QueryRoundTrip(t *testing.T) {
	special := "a b/c%d,e 中文"
	specialItems := []any{"a b", "c/d", "100%", "x|y;z", "中文"}
	// form non-explode object: values/keys must not contain ",".
	specialObjectForm := orderedProps{
		{"k space", "a b/c%d;e 中文"},
		{"sl/ash", "x|y;z"},
		{"pct%", "100%ok"},
		{"non ascii", "值"},
	}
	specialObject := orderedProps{
		{"k space", "a b/c%d;e 中文"},
		{"sl/ash", "x|y;z"},
		{"pct%", "100%ok"},
		{"non ascii", "值"},
	}

	stringSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}
	stringArraySchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: stringSchema,
	}}
	objectSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:                 &openapi3.Types{"object"},
		AdditionalProperties: openapi3.AdditionalProperties{Schema: stringSchema},
	}}
	explicitObjectSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: map[string]*openapi3.SchemaRef{
			"k space":   stringSchema,
			"sl/ash":    stringSchema,
			"pct%":      stringSchema,
			"non ascii": stringSchema,
		},
	}}
	// spaceDelimited items must not contain a raw space; pipeDelimited items
	// must not contain a raw pipe.
	spaceItems := []any{"a,b", "c/d", "100%", "x|y;z", "中"}
	pipeItems := []any{"a b", "c/d", "100%", "x,y;z", "中文"}

	cases := []struct {
		name   string
		param  *openapi3.Parameter
		schema *openapi3.SchemaRef
		value  any
	}{
		{"form primitive", &openapi3.Parameter{Name: "p", In: "query", Style: "form", Explode: boolPtr(true)}, stringSchema, special},
		{"form array explode", &openapi3.Parameter{Name: "p", In: "query", Style: "form", Explode: boolPtr(true)}, stringArraySchema, specialItems},
		{"form object explode", &openapi3.Parameter{Name: "p", In: "query", Style: "form", Explode: boolPtr(true)}, explicitObjectSchema, specialObject},
		{"form object", &openapi3.Parameter{Name: "p", In: "query", Style: "form", Explode: boolPtr(false)}, explicitObjectSchema, specialObjectForm},
		{"spaceDelimited array", &openapi3.Parameter{Name: "p", In: "query", Style: "spaceDelimited", Explode: boolPtr(false)}, stringArraySchema, spaceItems},
		{"pipeDelimited array", &openapi3.Parameter{Name: "p", In: "query", Style: "pipeDelimited", Explode: boolPtr(false)}, stringArraySchema, pipeItems},
		{"deepObject object", &openapi3.Parameter{Name: "p", In: "query", Style: "deepObject", Explode: boolPtr(true)}, objectSchema, specialObject},
	}
	// form non-explode array cannot carry an item containing the comma
	// delimiter, so it is covered with comma-free items.
	plainItems := []any{"a b", "c/d", "100%", "x|y;z", "中文"}
	cases = append(cases, struct {
		name   string
		param  *openapi3.Parameter
		schema *openapi3.SchemaRef
		value  any
	}{"form array", &openapi3.Parameter{Name: "p", In: "query", Style: "form", Explode: boolPtr(false)}, stringArraySchema, plainItems})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.param.Schema = tc.schema
			enc, err := EncodeParameter(tc.param, tc.value)
			require.NoError(t, err)

			var gotValue any
			var found bool
			var decodeErr error
			r := mux.NewRouter().UseEncodedPath()
			r.HandleFunc("/resource", func(w http.ResponseWriter, r *http.Request) {
				gotValue, found, decodeErr = decodeStyledParameter(tc.param, &RequestValidationInput{
					Request:    r,
					PathParams: mux.Vars(r),
				})
				w.WriteHeader(http.StatusNoContent)
			})
			srv := httptest.NewServer(r)
			defer srv.Close()

			reqURL := srv.URL + "/resource?" + enc.Query[0]
			for _, pair := range enc.Query[1:] {
				reqURL += "&" + pair
			}
			t.Logf("wire: %s", reqURL)
			resp, err := http.Get(reqURL)
			require.NoError(t, err)
			resp.Body.Close()

			require.NoError(t, err)
			t.Logf("decoded=%#v found=%v err=%v", gotValue, found, decodeErr)
			require.NoError(t, decodeErr)
			require.True(t, found)
			requireDecodedValue(t, tc.value, gotValue)
		})
	}
}

// requireDecodedValue compares the typed input against the decoder output.
// orderedProps expands to the map the decoder returns.
func requireDecodedValue(t *testing.T, want, got any) {
	t.Helper()
	if w, ok := want.(orderedProps); ok {
		m := make(map[string]any, len(w))
		for _, p := range w {
			m[p.key] = p.value
		}
		require.Equal(t, m, got)
		return
	}
	require.Equal(t, want, got)
}

// TestEncodeParameter_QueryEscaping verifies keys and values are escaped
// independently and delimiters inside values do not split the structure.
func TestEncodeParameter_QueryEscaping(t *testing.T) {
	t.Run("comma inside a form-explode item is not an array", func(t *testing.T) {
		p := &openapi3.Parameter{Name: "p", In: "query", Style: "form", Explode: boolPtr(true)}
		enc, err := EncodeParameter(p, []any{"a,b", "c"})
		require.NoError(t, err)
		require.Equal(t, []string{"p=a%2Cb", "p=c"}, enc.Query)

		values, err := url.ParseQuery("p=a%2Cb&p=c")
		require.NoError(t, err)
		require.Equal(t, []string{"a,b", "c"}, values["p"])
	})

	t.Run("ampersand and equals inside a value stay escaped", func(t *testing.T) {
		p := &openapi3.Parameter{Name: "p", In: "query"}
		enc, err := EncodeParameter(p, "a&b=c")
		require.NoError(t, err)
		require.Equal(t, "p=a%26b%3Dc", enc.Query[0])
		values, err := url.ParseQuery(enc.Query[0])
		require.NoError(t, err)
		require.Equal(t, "a&b=c", values.Get("p"))
	})

	t.Run("space is %20 not plus", func(t *testing.T) {
		p := &openapi3.Parameter{Name: "p", In: "query"}
		enc, err := EncodeParameter(p, "a b")
		require.NoError(t, err)
		require.Equal(t, "p=a%20b", enc.Query[0])
	})
}

// TestEncodeParameter_PathRoundTrip encodes each path style through a
// gorilla/mux router with UseEncodedPath and decodes it with the package
// decoder. Values cover space, slash, percent, comma and non-ASCII. The mux
// vars remain percent-encoded, so the test unescapes them exactly as a client
// substitution does when expanding the template.
func TestEncodeParameter_PathRoundTrip(t *testing.T) {
	special := "a b/c%d,e 中文"
	items := []any{"a b", "c/d", "100%", "x;y", "中文"}
	matrixExplodeItems := []any{"a b", "c/d", "100%", "x.y", "中文"}
	object := orderedProps{
		{"role", "a/b"},
		{"who", "x-y"},
		{"lang", "中文"},
	}

	stringSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}
	stringArraySchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: stringSchema,
	}}
	objectSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: map[string]*openapi3.SchemaRef{
			"role": stringSchema,
			"who":  stringSchema,
			"lang": stringSchema,
		},
	}}

	cases := []struct {
		name   string
		param  *openapi3.Parameter
		schema *openapi3.SchemaRef
		value  any
	}{
		{"simple primitive", param("path", "p", "simple", boolPtr(false)), stringSchema, special},
		{"label primitive", param("path", "p", "label", boolPtr(false)), stringSchema, special},
		{"matrix primitive", param("path", "p", "matrix", boolPtr(false)), stringSchema, special},
		{"simple array", param("path", "p", "simple", boolPtr(false)), stringArraySchema, items},
		{"label array", param("path", "p", "label", boolPtr(false)), stringArraySchema, items},
		{"label array explode", param("path", "p", "label", boolPtr(true)), stringArraySchema, items},
		{"matrix array", param("path", "p", "matrix", boolPtr(false)), stringArraySchema, items},
		{"matrix array explode", param("path", "p", "matrix", boolPtr(true)), stringArraySchema, matrixExplodeItems},
		{"simple object", param("path", "p", "simple", boolPtr(false)), objectSchema, object},
		{"simple object explode", param("path", "p", "simple", boolPtr(true)), objectSchema, object},
		{"label object", param("path", "p", "label", boolPtr(false)), objectSchema, object},
		{"label object explode", param("path", "p", "label", boolPtr(true)), objectSchema, object},
		{"matrix object", param("path", "p", "matrix", boolPtr(false)), objectSchema, object},
		{"matrix object explode", param("path", "p", "matrix", boolPtr(true)), objectSchema, object},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.param.Schema = tc.schema
			tc.param.Required = true
			enc, err := EncodeParameter(tc.param, tc.value)
			require.NoError(t, err)

			var gotValue any
			var found bool
			var decodeErr error
			r := mux.NewRouter().UseEncodedPath()
			r.HandleFunc("/resource/{p}", func(w http.ResponseWriter, r *http.Request) {
				vars := mux.Vars(r)
				// The decoder receives unescaped path vars, as produced by the
				// standard net/http URL.Path after template expansion.
				unescaped, uerr := url.PathUnescape(vars["p"])
				require.NoError(t, uerr)
				vars["p"] = unescaped
				gotValue, found, decodeErr = decodeStyledParameter(tc.param, &RequestValidationInput{
					Request:    r,
					PathParams: vars,
				})
				w.WriteHeader(http.StatusNoContent)
			})
			srv := httptest.NewServer(r)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/resource/" + enc.PathSegment)
			require.NoError(t, err)
			resp.Body.Close()

			require.NoError(t, decodeErr)
			require.True(t, found)
			requireDecodedValue(t, tc.value, gotValue)
		})
	}
}

// TestEncodeParameter_HeaderRoundTrip encodes simple header primitives,
// arrays and objects and decodes them from the received HTTP request.
func TestEncodeParameter_HeaderRoundTrip(t *testing.T) {
	stringSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}
	stringArraySchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: stringSchema,
	}}
	objectSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: map[string]*openapi3.SchemaRef{
			"role": stringSchema,
			"lang": stringSchema,
		},
	}}

	cases := []struct {
		name   string
		param  *openapi3.Parameter
		schema *openapi3.SchemaRef
		value  any
	}{
		{"primitive", param("header", "X-Param", "simple", boolPtr(false)), stringSchema, "blue;black=1 / 100%"},
		{"array", param("header", "X-Param", "simple", boolPtr(false)), stringArraySchema, []any{"blue", "bl ack", "100%"}},
		{"object", param("header", "X-Param", "simple", boolPtr(false)), objectSchema, orderedProps{{"role", "a;b"}, {"lang", "en"}}},
		{"object explode", param("header", "X-Param", "simple", boolPtr(true)), objectSchema, orderedProps{{"role", "a;b"}, {"lang", "en"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.param.Schema = tc.schema
			enc, err := EncodeParameter(tc.param, tc.value)
			require.NoError(t, err)

			var gotValue any
			var found bool
			var decodeErr error
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotValue, found, decodeErr = decodeStyledParameter(tc.param, &RequestValidationInput{Request: r})
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
			require.NoError(t, err)
			req.Header.Set(enc.HeaderName, enc.HeaderValue)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()

			require.NoError(t, decodeErr)
			require.True(t, found)
			requireDecodedValue(t, tc.value, gotValue)
		})
	}
}

// TestEncodeParameter_CookieRoundTrip encodes form cookie primitives, arrays
// and objects and decodes them from the received Cookie header.
func TestEncodeParameter_CookieRoundTrip(t *testing.T) {
	stringSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}
	stringArraySchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: stringSchema,
	}}
	objectSchema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"object"},
		Properties: map[string]*openapi3.SchemaRef{
			"a": stringSchema,
			"b": stringSchema,
		},
	}}

	cases := []struct {
		name   string
		param  *openapi3.Parameter
		schema *openapi3.SchemaRef
		value  any
	}{
		{"primitive", param("cookie", "c", "form", boolPtr(false)), stringSchema, "blue-black=1/x%y"},
		{"array", param("cookie", "c", "form", boolPtr(false)), stringArraySchema, []any{"blue", "black", "100%"}},
		{"object", param("cookie", "c", "form", boolPtr(false)), objectSchema, orderedProps{{"a", "x/y"}, {"b", "p%q"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.param.Schema = tc.schema
			enc, err := EncodeParameter(tc.param, tc.value)
			require.NoError(t, err)

			var gotValue any
			var found bool
			var decodeErr error
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotValue, found, decodeErr = decodeStyledParameter(tc.param, &RequestValidationInput{Request: r})
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
			require.NoError(t, err)
			req.AddCookie(&http.Cookie{Name: enc.CookieName, Value: enc.CookieValue})
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			resp.Body.Close()

			require.NoError(t, decodeErr)
			require.True(t, found)
			requireDecodedValue(t, tc.value, gotValue)
		})
	}
}
