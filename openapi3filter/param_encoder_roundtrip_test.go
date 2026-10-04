package openapi3filter

import (
	"bufio"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
)

// Special values that exercise percent-encoding and structural-delimiter
// escaping: spaces, slashes, percent signs, commas, non-ASCII text and
// combinations thereof.
var roundtripStrings = []string{
	"plain",
	"a b",
	"a/b",
	"100%",
	"100%25",
	"a,b",
	"héllo wörld/100%",
	"  spaced  ",
	"slash/,comma,%percent",
	"emoji😀tab",
	"q=u&e+r",
	"pipe|delim",
	"dot.dot",
	"semi;colon=eq",
	"brack[et]",
}

func decodeOne(t *testing.T, param *openapi3.Parameter, value any) any {
	t.Helper()
	enc, err := EncodeParameter(param, value)
	require.NoError(t, err)

	sm, err := param.SerializationMethod()
	require.NoError(t, err)

	switch param.In {
	case openapi3.ParameterInPath:
		// Routers hand already-unescaped segments to the decoder; emulate that
		// by decoding the whole segment (structural prefixes are unreserved).
		decodedSegment, err := url.PathUnescape(enc.PathSegment)
		require.NoError(t, err)
		dec := &pathParamDecoder{pathParams: map[string]string{param.Name: decodedSegment}}
		out, _, err := decodeValue(dec, param.Name, sm, param.Schema, false)
		require.NoError(t, err)
		return out
	case openapi3.ParameterInQuery:
		// Build the raw query the same way SetOnRequest does, then parse it to
		// exercise real percent-decoding (keys and values separately).
		parts := make([]string, 0, len(enc.QueryPairs))
		for _, pair := range enc.QueryPairs {
			parts = append(parts, pair.QueryKey+"="+escapeQueryValue(pair.QueryValue, param.AllowReserved))
		}
		values, err := url.ParseQuery(joinParts(parts))
		require.NoError(t, err)
		dec := &urlValuesDecoder{values: values}
		out, _, err := decodeValue(dec, param.Name, sm, param.Schema, false)
		require.NoError(t, err)
		return out
	case openapi3.ParameterInHeader:
		req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
		require.NoError(t, err)
		require.NoError(t, enc.SetOnRequest(req))
		dec := &headerParamDecoder{header: req.Header}
		out, _, err := decodeValue(dec, param.Name, sm, param.Schema, false)
		require.NoError(t, err)
		return out
	case openapi3.ParameterInCookie:
		req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
		require.NoError(t, err)
		require.NoError(t, enc.SetOnRequest(req))
		// Feed the serialized Cookie header back into a request for decoding.
		served, err := http.ReadRequest(bufio.NewReader(strings.NewReader("GET / HTTP/1.1\r\nHost: example.com\r\nCookie: " + req.Header.Get("Cookie") + "\r\n\r\n")))
		require.NoError(t, err)
		dec := &cookieParamDecoder{req: served}
		out, _, err := decodeValue(dec, param.Name, sm, param.Schema, false)
		require.NoError(t, err)
		return out
	}
	return nil
}

func joinParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "&"
		}
		out += p
	}
	return out
}

func paramWith(in, name, style string, explode bool, schema *openapi3.SchemaRef) *openapi3.Parameter {
	return &openapi3.Parameter{
		Name:    name,
		In:      in,
		Style:   style,
		Explode: &explode,
		Schema:  schema,
	}
}

// TestEncodeDecodeRoundtrip_Primitive verifies encode->decode identity for
// every primitive-supporting style/location.
func TestEncodeDecodeRoundtrip_Primitive(t *testing.T) {
	primitiveStyles := []struct {
		in, style string
		explode   bool
	}{
		{"path", "simple", false},
		{"path", "simple", true},
		{"path", "label", false},
		{"path", "label", true},
		{"path", "matrix", false},
		{"path", "matrix", true},
		{"query", "form", false},
		{"query", "form", true},
		{"header", "simple", false},
		{"header", "simple", true},
		{"cookie", "form", false},
	}
	schema := &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}
	for _, tc := range primitiveStyles {
		for _, s := range roundtripStrings {
			// Cookies cannot represent spaces, non-ASCII and other special
			// bytes; the encoder rejects them explicitly (covered elsewhere).
			if tc.in == "cookie" && !cookieEncodable(s) {
				param := paramWith(tc.in, "p", tc.style, tc.explode, schema)
				_, err := EncodeParameter(param, s)
				require.Error(t, err, "cookie %q must be rejected", s)
				continue
			}
			// Header values cannot carry control characters; our strings do
			// not contain any, but leading/trailing whitespace is unsafe only
			// for arrays.
			param := paramWith(tc.in, "p", tc.style, tc.explode, schema)
			got := decodeOne(t, param, s)
			require.Equal(t, s, got, "%s/%s explode=%v value=%q", tc.in, tc.style, tc.explode, s)
		}
	}
}

func cookieEncodable(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e {
			return false
		}
		switch c {
		case ' ', '\t', '"', ';', '\\':
			return false
		}
	}
	return true
}

// roundtripArrayItems excludes values that structurally collide with the
// delimiter of the style under test (those are covered by the rejection tests).
func arrayItemsFor(in, style string, explode bool) []string {
	items := make([]string, 0, len(roundtripStrings))
	for _, s := range roundtripStrings {
		if in == "cookie" && !cookieEncodable(s) {
			continue
		}
		var delim string
		switch {
		case in == "path" && style == "matrix" && explode:
			// only an exact ";p=" collision is structurally ambiguous
			if containsAll(s, ";p=") {
				continue
			}
		case in == "path" && style == "label" && explode:
			delim = "."
		case in == "path":
			// simple (both explode values), label/matrix non-explode use ","
			delim = ","
		case in == "header" || in == "cookie":
			delim = ","
		case in == "query" && !explode:
			switch style {
			case "form":
				delim = ","
			case "spaceDelimited":
				delim = " "
			case "pipeDelimited":
				delim = "|"
			}
		}
		if delim != "" && containsAll(s, delim) {
			continue
		}
		// Header parsing trims OWS around list elements.
		if in == "header" && s != trimSpace(s) {
			continue
		}
		items = append(items, s)
	}
	return items
}

func containsAll(s, sub string) bool { return len(sub) > 0 && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpaceByte(s[start]) {
		start++
	}
	for end > start && isSpaceByte(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' }

// TestEncodeDecodeRoundtrip_Array verifies encode->decode identity for arrays.
func TestEncodeDecodeRoundtrip_Array(t *testing.T) {
	arrayStyles := []struct {
		in, style string
		explode   bool
	}{
		{"path", "simple", false},
		{"path", "simple", true},
		{"path", "label", false},
		{"path", "label", true},
		{"path", "matrix", false},
		{"path", "matrix", true},
		{"query", "form", false},
		{"query", "form", true},
		{"query", "spaceDelimited", false},
		{"query", "spaceDelimited", true},
		{"query", "pipeDelimited", false},
		{"query", "pipeDelimited", true},
		{"header", "simple", false},
		{"header", "simple", true},
		{"cookie", "form", false},
	}
	schema := &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
	}}
	for _, tc := range arrayStyles {
		items := arrayItemsFor(tc.in, tc.style, tc.explode)
		param := paramWith(tc.in, "p", tc.style, tc.explode, schema)
		got := decodeOne(t, param, items)
		require.Equal(t, toAnySlice(items), got, "%s/%s explode=%v", tc.in, tc.style, tc.explode)
	}
}

func toAnySlice(items []string) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

// TestEncodeDecodeRoundtrip_Object verifies encode->decode identity for flat
// objects, including deepObject.
func TestEncodeDecodeRoundtrip_Object(t *testing.T) {
	// Values are chosen to avoid each style's structural delimiter; property
	// names avoid delimiters too.
	obj := map[string]string{
		"alpha": "a b/c%",
		"beta":  "héllo,世界",
		"gamma": "val|with=stuff",
		"delta": "plain123",
	}

	schema := &openapi3.SchemaRef{Value: openapi3.NewObjectSchema().
		WithProperty("alpha", openapi3.NewStringSchema()).
		WithProperty("beta", openapi3.NewStringSchema()).
		WithProperty("gamma", openapi3.NewStringSchema()).
		WithProperty("delta", openapi3.NewStringSchema())}

	objectStyles := []struct {
		in, style string
		explode   bool
		obj       map[string]string
	}{
		{"path", "simple", false, noCommaObj(obj)},
		{"path", "simple", true, noCommaEqObj(obj)}, // exploded pairs are comma-separated, "=" separates name/value
		{"path", "label", false, noCommaObj(obj)},
		{"path", "label", true, noDotObj(obj)},
		{"path", "matrix", false, noCommaObj(obj)},
		{"path", "matrix", true, noSemiEqObj(obj)},
		{"query", "form", false, noCommaObj(obj)},
		{"query", "form", true, obj},
		{"query", "deepObject", true, noBracketObj(obj)},
		{"header", "simple", false, noCommaObj(obj)},
		{"header", "simple", true, noCommaEqObj(obj)}, // header pairs are comma-separated, "=" separates name/value
		{"cookie", "form", false, cookieObj(obj)},
	}
	for _, tc := range objectStyles {
		param := paramWith(tc.in, "p", tc.style, tc.explode, schema)
		got := decodeOne(t, param, tc.obj)
		require.Equal(t, toAnyMap(tc.obj), got, "%s/%s explode=%v", tc.in, tc.style, tc.explode)
	}
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func filterObj(m map[string]string, keep func(string, string) bool) map[string]string {
	out := make(map[string]string)
	for k, v := range m {
		if keep(k, v) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		out["delta"] = "plain123"
	}
	return out
}

func noCommaObj(m map[string]string) map[string]string {
	return filterObj(m, func(k, v string) bool {
		return !containsAll(k, ",") && !containsAll(v, ",")
	})
}

func noCommaEqObj(m map[string]string) map[string]string {
	return filterObj(m, func(k, v string) bool {
		return !containsAll(k, ",") && !containsAll(v, ",") && !containsAll(v, "=") && !containsAll(k, "=")
	})
}

func noDotObj(m map[string]string) map[string]string {
	return filterObj(m, func(k, v string) bool {
		return !containsAll(k, ".") && !containsAll(v, ".") && !containsAll(v, "=") && !containsAll(k, "=")
	})
}

func noSemiEqObj(m map[string]string) map[string]string {
	return filterObj(m, func(k, v string) bool {
		return !containsAll(k, ";") && !containsAll(v, ";") && !containsAll(v, "=") && !containsAll(k, "=")
	})
}

func noBracketObj(m map[string]string) map[string]string {
	return filterObj(m, func(k, v string) bool {
		return !containsAll(k, "[") && !containsAll(k, "]")
	})
}

func cookieObj(m map[string]string) map[string]string {
	return filterObj(m, func(k, v string) bool {
		return cookieEncodable(k) && cookieEncodable(v) && !containsAll(v, ",") && !containsAll(k, ",")
	})
}
