package openapi3filter

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
)

func boolPtr(b bool) *bool { return &b }

func param(in, name, style string, explode *bool) *openapi3.Parameter {
	return &openapi3.Parameter{Name: name, In: in, Style: style, Explode: explode}
}

// orderedProps is an object value with an explicit property order. The
// encoder accepts it through OrderedObjectValue; the test type keeps the
// spec examples' prescribed output order (R, G, B).
type orderedProps []orderedProp
type orderedProp struct {
	key   string
	value any
}

func mapOrderedProps(kv ...any) orderedProps {
	if len(kv)%2 != 0 {
		panic("mapOrderedProps requires pairs")
	}
	out := make(orderedProps, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		out = append(out, orderedProp{kv[i].(string), kv[i+1]})
	}
	return out
}

func (o orderedProps) OrderedObjectValue() [][2]string {
	out := make([][2]string, 0, len(o))
	for _, p := range o {
		s, err := primitiveToString(p.value)
		if err != nil {
			panic(err)
		}
		out = append(out, [2]string{p.key, s})
	}
	return out
}

// The fixed examples from OpenAPI 3.0.3 section 4.8.12.4 "Parameter Object
// Examples".
func TestEncodeParameter_SpecExamples(t *testing.T) {
	color := []any{"blue", "black", "brown"}
	colorMap := mapOrderedProps("R", 100, "G", 200, "B", 150)

	type tc struct {
		name  string
		param *openapi3.Parameter
		value any
		want  *EncodedParameter
	}
	cases := []tc{
		// path
		{"path simple primitive", param("path", "id", "simple", boolPtr(false)), 5, &EncodedParameter{In: "path", Name: "id", PathSegment: "5"}},
		{"path simple primitive explode", param("path", "id", "simple", boolPtr(true)), 5, &EncodedParameter{In: "path", Name: "id", PathSegment: "5"}},
		{"path simple array", param("path", "id", "simple", boolPtr(false)), color, &EncodedParameter{In: "path", Name: "id", PathSegment: "blue,black,brown"}},
		{"path simple array explode", param("path", "id", "simple", boolPtr(true)), color, &EncodedParameter{In: "path", Name: "id", PathSegment: "blue,black,brown"}},
		{"path simple object", param("path", "id", "simple", boolPtr(false)), colorMap, &EncodedParameter{In: "path", Name: "id", PathSegment: "R,100,G,200,B,150"}},
		{"path simple object explode", param("path", "id", "simple", boolPtr(true)), colorMap, &EncodedParameter{In: "path", Name: "id", PathSegment: "R=100,G=200,B=150"}},

		{"path label primitive", param("path", "id", "label", boolPtr(false)), 5, &EncodedParameter{In: "path", Name: "id", PathSegment: ".5"}},
		{"path label primitive explode", param("path", "id", "label", boolPtr(true)), 5, &EncodedParameter{In: "path", Name: "id", PathSegment: ".5"}},
		{"path label array", param("path", "id", "label", boolPtr(false)), color, &EncodedParameter{In: "path", Name: "id", PathSegment: ".blue,black,brown"}},
		{"path label array explode", param("path", "id", "label", boolPtr(true)), color, &EncodedParameter{In: "path", Name: "id", PathSegment: ".blue.black.brown"}},
		{"path label object", param("path", "id", "label", boolPtr(false)), colorMap, &EncodedParameter{In: "path", Name: "id", PathSegment: ".R,100,G,200,B,150"}},
		{"path label object explode", param("path", "id", "label", boolPtr(true)), colorMap, &EncodedParameter{In: "path", Name: "id", PathSegment: ".R=100.G=200.B=150"}},

		{"path matrix primitive", param("path", "id", "matrix", boolPtr(false)), 5, &EncodedParameter{In: "path", Name: "id", PathSegment: ";id=5"}},
		{"path matrix primitive explode", param("path", "id", "matrix", boolPtr(true)), 5, &EncodedParameter{In: "path", Name: "id", PathSegment: ";id=5"}},
		{"path matrix array", param("path", "id", "matrix", boolPtr(false)), color, &EncodedParameter{In: "path", Name: "id", PathSegment: ";id=blue,black,brown"}},
		{"path matrix array explode", param("path", "id", "matrix", boolPtr(true)), color, &EncodedParameter{In: "path", Name: "id", PathSegment: ";id=blue;id=black;id=brown"}},
		{"path matrix object", param("path", "id", "matrix", boolPtr(false)), colorMap, &EncodedParameter{In: "path", Name: "id", PathSegment: ";id=R,100,G,200,B,150"}},
		{"path matrix object explode", param("path", "id", "matrix", boolPtr(true)), colorMap, &EncodedParameter{In: "path", Name: "id", PathSegment: ";R=100;G=200;B=150"}},

		// query
		{"query form primitive", param("query", "id", "form", boolPtr(true)), 5, &EncodedParameter{In: "query", Name: "id", Query: []string{"id=5"}}},
		{"query form primitive no explode", param("query", "id", "form", boolPtr(false)), 5, &EncodedParameter{In: "query", Name: "id", Query: []string{"id=5"}}},
		{"query form array", param("query", "id", "form", boolPtr(false)), color, &EncodedParameter{In: "query", Name: "id", Query: []string{"id=blue,black,brown"}}},
		{"query form array explode", param("query", "id", "form", boolPtr(true)), color, &EncodedParameter{In: "query", Name: "id", Query: []string{"id=blue", "id=black", "id=brown"}}},
		{"query form object", param("query", "id", "form", boolPtr(false)), colorMap, &EncodedParameter{In: "query", Name: "id", Query: []string{"id=R,100,G,200,B,150"}}},
		{"query form object explode", param("query", "id", "form", boolPtr(true)), colorMap, &EncodedParameter{In: "query", Name: "id", Query: []string{"R=100", "G=200", "B=150"}}},

		{"query spaceDelimited array", param("query", "id", "spaceDelimited", boolPtr(false)), color, &EncodedParameter{In: "query", Name: "id", Query: []string{"id=blue%20black%20brown"}}},
		{"query spaceDelimited array explode", param("query", "id", "spaceDelimited", boolPtr(true)), color, &EncodedParameter{In: "query", Name: "id", Query: []string{"id=blue", "id=black", "id=brown"}}},
		{"query pipeDelimited array", param("query", "id", "pipeDelimited", boolPtr(false)), color, &EncodedParameter{In: "query", Name: "id", Query: []string{"id=blue|black|brown"}}},
		{"query pipeDelimited array explode", param("query", "id", "pipeDelimited", boolPtr(true)), color, &EncodedParameter{In: "query", Name: "id", Query: []string{"id=blue", "id=black", "id=brown"}}},
		{"query deepObject object explode", param("query", "id", "deepObject", boolPtr(true)), colorMap, &EncodedParameter{In: "query", Name: "id", Query: []string{"id[R]=100", "id[G]=200", "id[B]=150"}}},

		// header
		{"header simple primitive", param("header", "X-MyHeader", "simple", boolPtr(false)), 5, &EncodedParameter{In: "header", Name: "X-MyHeader", HeaderName: "X-Myheader", HeaderValue: "5"}},
		{"header simple array", param("header", "X-MyHeader", "simple", boolPtr(false)), color, &EncodedParameter{In: "header", Name: "X-MyHeader", HeaderName: "X-Myheader", HeaderValue: "blue,black,brown"}},
		{"header simple object", param("header", "X-MyHeader", "simple", boolPtr(false)), colorMap, &EncodedParameter{In: "header", Name: "X-MyHeader", HeaderName: "X-Myheader", HeaderValue: "R,100,G,200,B,150"}},
		{"header simple object explode", param("header", "X-MyHeader", "simple", boolPtr(true)), colorMap, &EncodedParameter{In: "header", Name: "X-MyHeader", HeaderName: "X-Myheader", HeaderValue: "R=100,G=200,B=150"}},

		// cookie
		{"cookie form primitive", param("cookie", "id", "form", boolPtr(false)), 5, &EncodedParameter{In: "cookie", Name: "id", CookieName: "id", CookieValue: "5"}},
		{"cookie form array", param("cookie", "id", "form", boolPtr(false)), color, &EncodedParameter{In: "cookie", Name: "id", CookieName: "id", CookieValue: "blue,black,brown"}},
		{"cookie form object", param("cookie", "id", "form", boolPtr(false)), colorMap, &EncodedParameter{In: "cookie", Name: "id", CookieName: "id", CookieValue: "R,100,G,200,B,150"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeParameter(tc.param, tc.value)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	// Defaults: omitted style/explode resolve per location.
	t.Run("default style and explode", func(t *testing.T) {
		p := &openapi3.Parameter{Name: "id", In: "query"}
		got, err := EncodeParameter(p, color)
		require.NoError(t, err)
		require.Equal(t, []string{"id=blue", "id=black", "id=brown"}, got.Query)

		p = &openapi3.Parameter{Name: "id", In: "path", Required: true}
		got, err = EncodeParameter(p, color)
		require.NoError(t, err)
		require.Equal(t, "blue,black,brown", got.PathSegment)
	})

	_ = url.Values{}
	_ = http.Header{}
}
