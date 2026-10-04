package openapi3link_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3link"
)

func TestSlashedObjectKey(t *testing.T) {
	doc := expressionDoc()
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	// A property name containing "/" is escaped as "~1" in a JSON Pointer.
	exchange := &openapi3link.Exchange{
		Response: openapi3link.ResponseData{
			StatusCode: 200,
			Body: map[string]any{
				"a/b": "slashed",
				"m": map[string]any{
					"x/y": map[string]any{"~tilde": "deep"},
				},
			},
		},
	}

	cases := map[string]string{
		"$response.body#/a~1b":           "slashed",
		"$response.body#/m/x~1y/~0tilde": "deep",
	}
	for expr, want := range cases {
		link := &openapi3.Link{
			OperationID: "sink",
			Parameters:  map[string]any{"query.p": expr},
		}
		plan, err := docs.Evaluate(link, exchange, "")
		require.NoError(t, err, "expression %s", expr)
		assert.Equal(t, want, plan.Parameter(openapi3.ParameterInQuery, "p").Value)
	}
}

func TestMissingVersusExplicitNull(t *testing.T) {
	doc := expressionDoc()
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	exchange := &openapi3link.Exchange{
		Response: openapi3link.ResponseData{
			StatusCode: 200,
			Body:       map[string]any{"present": nil},
		},
	}

	t.Run("missing fragment is an error, not a constant", func(t *testing.T) {
		link := &openapi3.Link{
			OperationID: "sink",
			Parameters:  map[string]any{"query.p": "$response.body#/absent"},
		}
		_, err := docs.Evaluate(link, exchange, "")
		require.Error(t, err)

		var linkErr *openapi3link.LinkError
		require.True(t, errors.As(err, &linkErr))
		var missing *openapi3link.MissingValueError
		assert.True(t, errors.As(err, &missing))
		assert.Equal(t, "$response.body#/absent", missing.Expression)
	})

	t.Run("explicit null is reported distinctly", func(t *testing.T) {
		link := &openapi3.Link{
			OperationID: "sink",
			Parameters:  map[string]any{"query.p": "$response.body#/present"},
		}
		plan, err := docs.Evaluate(link, exchange, "")
		require.NoError(t, err)
		got := plan.Parameter(openapi3.ParameterInQuery, "p")
		require.NotNil(t, got)
		assert.Nil(t, got.Value)
		assert.True(t, got.IsNull)
	})

	t.Run("explicit null rejected by non-nullable target", func(t *testing.T) {
		strictDoc := expressionDoc()
		for _, ref := range strictDoc.Paths.Value("/sink/{p}").Get.Parameters {
			if ref.Value.In == openapi3.ParameterInQuery {
				ref.Value.Schema = &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}
			}
		}
		strictDocs, err := openapi3link.NewDocuments(openapi3link.Document{T: strictDoc})
		require.NoError(t, err)

		link := &openapi3.Link{
			OperationID: "sink",
			Parameters:  map[string]any{"query.p": "$response.body#/present"},
		}
		_, err = strictDocs.Evaluate(link, exchange, "")
		require.Error(t, err)
		var nullErr *openapi3link.NullNotAllowedError
		assert.True(t, errors.As(err, &nullErr))
	})
}

func TestPercentEncodedQueryToken(t *testing.T) {
	doc := expressionDoc()
	for _, ref := range doc.Paths.Value("/sink/{p}").Get.Parameters {
		if ref.Value.In == openapi3.ParameterInQuery {
			ref.Value.Name = "weird/name"
		}
	}
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	exchange := &openapi3link.Exchange{
		Request: openapi3link.RequestData{
			QueryParams: map[string]any{"weird/name": "decoded"},
		},
	}
	link := &openapi3.Link{
		OperationID: "sink",
		// $request.query.weird%2Fname
		Parameters: map[string]any{"query.weird/name": "$request.query.weird%2Fname"},
	}
	plan, err := docs.Evaluate(link, exchange, "")
	require.NoError(t, err)
	assert.Equal(t, "decoded", plan.Parameter(openapi3.ParameterInQuery, "weird/name").Value)
}

func TestLiteralNullAndNullableTarget(t *testing.T) {
	doc := expressionDoc()
	nullableSchema := openapi3.NewStringSchema()
	nullableSchema.Nullable = true
	for _, ref := range doc.Paths.Value("/sink/{p}").Get.Parameters {
		if ref.Value.In == openapi3.ParameterInQuery {
			ref.Value.Schema = &openapi3.SchemaRef{Value: nullableSchema}
		}
	}
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	t.Run("explicit literal null accepted by nullable", func(t *testing.T) {
		link := &openapi3.Link{OperationID: "sink", Parameters: map[string]any{"query.p": nil}}
		plan, err := docs.Evaluate(link, &openapi3link.Exchange{}, "")
		require.NoError(t, err)
		got := plan.Parameter(openapi3.ParameterInQuery, "p")
		require.NotNil(t, got)
		assert.Nil(t, got.Value)
		assert.True(t, got.IsNull)
	})

	t.Run("expression null accepted by nullable", func(t *testing.T) {
		link := &openapi3.Link{OperationID: "sink", Parameters: map[string]any{"query.p": "$response.body#/present"}}
		exchange := &openapi3link.Exchange{Response: openapi3link.ResponseData{
			Body: map[string]any{"present": nil},
		}}
		plan, err := docs.Evaluate(link, exchange, "")
		require.NoError(t, err)
		assert.True(t, plan.Parameter(openapi3.ParameterInQuery, "p").IsNull)
	})
}

func TestInvalidExpressionFormsReportPosition(t *testing.T) {
	doc := expressionDoc()
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)
	exchange := &openapi3link.Exchange{
		Response: openapi3link.ResponseData{StatusCode: 200, Body: map[string]any{"id": "x"}},
	}

	cases := []struct {
		expr       string
		wantOffset int
		hint       string
	}{
		{"$", 1, "missing expression source"},
		{"$bogus", 1, "unsupported source"},
		{"$request.", 9, "missing message part"},
		{"$request.cookie.x", 9, "unsupported message part"},
		{"$request.path.", 10, "missing path parameter name"},
		{"$request.body#id", 13, "JSON Pointer"},
		{"$response.body#/a~2", 15, "invalid JSON Pointer"},
		{"$response.body#/id~", 15, "invalid JSON Pointer"},
		{"$request.query.x#/id", 16, "fragment is only valid on body"},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			link := &openapi3.Link{
				OperationID: "sink",
				Parameters:  map[string]any{"query.p": tc.expr},
			}
			_, err := docs.Evaluate(link, exchange, "")
			require.Error(t, err)

			var exprErr *openapi3link.ExpressionError
			require.True(t, errors.As(err, &exprErr), "expected ExpressionError, got %v", err)
			assert.Equal(t, tc.expr, exprErr.Expression)
			assert.Equal(t, tc.wantOffset, exprErr.Offset, "unexpected position")
			assert.Contains(t, exprErr.Error(), tc.hint)
		})
	}
}

func TestLiteralStaysLiteral(t *testing.T) {
	doc := expressionDoc()
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	// A string that merely looks dynamic but does not start with '$' is a
	// literal and must not be parsed or evaluated.
	link := &openapi3.Link{
		OperationID: "sink",
		Parameters:  map[string]any{"query.p": "{response.body#/id}"},
	}
	plan, err := docs.Evaluate(link, &openapi3link.Exchange{}, "")
	require.NoError(t, err)
	assert.Equal(t, "{response.body#/id}", plan.Parameter(openapi3.ParameterInQuery, "p").Value)
}

func TestAssignability(t *testing.T) {
	doc := expressionDoc()
	for _, ref := range doc.Paths.Value("/sink/{p}").Get.Parameters {
		if ref.Value.In == openapi3.ParameterInQuery {
			ref.Value.Schema = &openapi3.SchemaRef{Value: openapi3.NewIntegerSchema()}
		}
	}
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	exchange := &openapi3link.Exchange{
		Response: openapi3link.ResponseData{
			StatusCode: 200,
			Body:       map[string]any{"n": 7.0, "s": "seven"},
		},
	}

	t.Run("integer value accepted", func(t *testing.T) {
		link := &openapi3.Link{OperationID: "sink", Parameters: map[string]any{"query.p": "$response.body#/n"}}
		plan, err := docs.Evaluate(link, exchange, "")
		require.NoError(t, err)
		assert.Equal(t, 7.0, plan.Parameter(openapi3.ParameterInQuery, "p").Value)
	})

	t.Run("string into integer rejected without coercion", func(t *testing.T) {
		link := &openapi3.Link{OperationID: "sink", Parameters: map[string]any{"query.p": "$response.body#/s"}}
		_, err := docs.Evaluate(link, exchange, "")
		require.Error(t, err)
		var assignErr *openapi3link.AssignmentError
		assert.True(t, errors.As(err, &assignErr))
	})

	t.Run("literal outside enum rejected", func(t *testing.T) {
		enumDoc := expressionDoc()
		enumSchema := openapi3.NewStringSchema()
		enumSchema.Enum = []any{"small", "large"}
		for _, ref := range enumDoc.Paths.Value("/sink/{p}").Get.Parameters {
			if ref.Value.In == openapi3.ParameterInQuery {
				ref.Value.Schema = &openapi3.SchemaRef{Value: enumSchema}
			}
		}
		enumDocs, err := openapi3link.NewDocuments(openapi3link.Document{T: enumDoc})
		require.NoError(t, err)

		link := &openapi3.Link{OperationID: "sink", Parameters: map[string]any{"query.p": "huge"}}
		_, err = enumDocs.Evaluate(link, &openapi3link.Exchange{}, "")
		require.Error(t, err)
		var assignErr *openapi3link.AssignmentError
		assert.True(t, errors.As(err, &assignErr))
	})
}

func TestTargetParameterIdentity(t *testing.T) {
	doc := expressionDoc() // declares path.p, query.p, header.p
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	t.Run("bare name is ambiguous", func(t *testing.T) {
		link := &openapi3.Link{OperationID: "sink", Parameters: map[string]any{"p": "v"}}
		_, err := docs.Evaluate(link, &openapi3link.Exchange{}, "")
		require.Error(t, err)
		var targetErr *openapi3link.TargetParameterError
		require.True(t, errors.As(err, &targetErr))
		assert.Contains(t, targetErr.Error(), "ambiguous")
	})

	t.Run("unknown parameter", func(t *testing.T) {
		link := &openapi3.Link{OperationID: "sink", Parameters: map[string]any{"query.nope": "v"}}
		_, err := docs.Evaluate(link, &openapi3link.Exchange{}, "")
		require.Error(t, err)
		var targetErr *openapi3link.TargetParameterError
		assert.True(t, errors.As(err, &targetErr))
	})

	t.Run("qualified name resolves", func(t *testing.T) {
		link := &openapi3.Link{OperationID: "sink", Parameters: map[string]any{"header.p": "v"}}
		plan, err := docs.Evaluate(link, &openapi3link.Exchange{}, "")
		require.NoError(t, err)
		assert.Equal(t, "v", plan.Parameter(openapi3.ParameterInHeader, "p").Value)
	})
}
