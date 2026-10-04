package link_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3/link"
	"github.com/stretchr/testify/require"
)

func TestInvalidExpressionReportsPosition(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	cases := []struct {
		name string
		expr string
		pos  int
	}{
		{"unknown root", "$nope", 1},
		{"unsupported response part", "$response.query.expand", 9},
		{"bad body token", "$response.bodyx/id", 14},
		{"bad pointer", "$response.body#id", 15},
		{"bad escape", "$response.body#/a~x", 16},
		{"trailing junk", "$method/", 7},
		{"bare sign", "$", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &openapi3.Link{
				OperationID: "getWidget",
				Parameters:  map[string]any{"widgetId": tc.expr},
			}
			_, err := link.Evaluate(docs, link.Source{Document: doc}, l, createExchange())
			var ee *link.ExprError
			require.ErrorAs(t, err, &ee)
			require.Equal(t, tc.pos, ee.Pos, "reason: %s", ee.Reason)
			require.Equal(t, "widgetId", ee.At.ParamKey)
			require.Equal(t, tc.expr, ee.Expr)
		})
	}
}

func TestDollarStringIsNotConstant(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	// A plain literal passes through untouched.
	lit := &openapi3.Link{
		OperationID: "getWidget",
		Parameters:  map[string]any{"widgetId": "literal-id"},
	}
	plan, err := link.Evaluate(docs, link.Source{Document: doc}, lit, createExchange())
	require.NoError(t, err)
	require.Equal(t, "literal-id", plan.Parameters[0].Value)

	// An expression embedded inside a constant array is not silently
	// treated as a constant string.
	emb := &openapi3.Link{
		OperationID: "getWidget",
		Parameters:  map[string]any{"widgetId": []any{"$nope"}},
	}
	_, err = link.Evaluate(docs, link.Source{Document: doc}, emb, createExchange())
	var ee *link.ExprError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, "[0]", ee.At.Sub)
	require.Equal(t, "$nope", ee.Expr)
}

func TestMissingValue(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	l := &openapi3.Link{
		OperationID: "getWidget",
		Parameters:  map[string]any{"widgetId": "$response.body#/missing/key"},
	}
	_, err = link.Evaluate(docs, link.Source{Document: doc}, l, createExchange())
	var me *link.MissingError
	require.ErrorAs(t, err, &me)
	require.Equal(t, "widgetId", me.At.ParamKey)

	// Missing header and query are missing too, case-insensitive lookup
	// notwithstanding.
	_, _, err = link.EvaluateValue("$response.header.x-absent", createExchange())
	require.ErrorAs(t, err, &me)
	_, _, err = link.EvaluateValue("$request.query.absent", createExchange())
	require.ErrorAs(t, err, &me)
}

func TestDuplicateOperationID(t *testing.T) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "t", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/a", &openapi3.PathItem{
				Get: &openapi3.Operation{OperationID: "dup"},
			}),
			openapi3.WithPath("/b", &openapi3.PathItem{
				Post: &openapi3.Operation{OperationID: "dup"},
			}),
		),
	}
	_, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	var de *link.DuplicateOperationIDError
	require.ErrorAs(t, err, &de)
	require.Equal(t, "dup", de.ID)
}

func TestMissingOperationTarget(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	t.Run("unknown operationId", func(t *testing.T) {
		l := &openapi3.Link{OperationID: "doesNotExist"}
		_, err := link.Evaluate(docs, link.Source{Document: doc}, l, createExchange())
		var te *link.TargetError
		require.ErrorAs(t, err, &te)
	})

	t.Run("missing path in operationRef", func(t *testing.T) {
		l := &openapi3.Link{OperationRef: "#/paths/~1nope/get"}
		_, err := link.Evaluate(docs, link.Source{Document: doc, DocURL: "api.yaml"}, l, createExchange())
		var te *link.TargetError
		require.ErrorAs(t, err, &te)
		require.Contains(t, te.Error(), "does not exist")
	})

	t.Run("missing method in operationRef", func(t *testing.T) {
		l := &openapi3.Link{OperationRef: "#/paths/~1widgets/delete"}
		_, err := link.Evaluate(docs, link.Source{Document: doc, DocURL: "api.yaml"}, l, createExchange())
		var te *link.TargetError
		require.ErrorAs(t, err, &te)
		require.Contains(t, te.Error(), "no DELETE operation")
	})

	t.Run("unloaded external document", func(t *testing.T) {
		l := &openapi3.Link{OperationRef: "elsewhere.yaml#/paths/~1x/get"}
		_, err := link.Evaluate(docs, link.Source{Document: doc, DocURL: "api.yaml"}, l, createExchange())
		var te *link.TargetError
		require.ErrorAs(t, err, &te)
	})
}

func TestParameterIdentityAndAssignability(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	t.Run("unknown unqualified parameter", func(t *testing.T) {
		l := &openapi3.Link{
			OperationID: "getWidget",
			Parameters:  map[string]any{"nope": "$response.body#/id"},
		}
		_, err := link.Evaluate(docs, link.Source{Document: doc}, l, createExchange())
		var ue *link.UnassignableError
		require.ErrorAs(t, err, &ue)
		require.Equal(t, "nope", ue.At.ParamKey)
	})

	t.Run("unknown location", func(t *testing.T) {
		l := &openapi3.Link{
			OperationID: "getWidget",
			Parameters:  map[string]any{"cookie.widgetId": "$response.body#/id"},
		}
		_, err := link.Evaluate(docs, link.Source{Document: doc}, l, createExchange())
		var ue *link.UnassignableError
		require.ErrorAs(t, err, &ue)
		require.Contains(t, ue.Error(), "no cookie parameter")
	})

	t.Run("qualified resolves", func(t *testing.T) {
		l := &openapi3.Link{
			OperationID: "getWidget",
			Parameters:  map[string]any{"path.widgetId": "$response.body#/id"},
		}
		plan, err := link.Evaluate(docs, link.Source{Document: doc}, l, createExchange())
		require.NoError(t, err)
		require.Equal(t, "path", plan.Parameters[0].In)
		require.Equal(t, "widgetId", plan.Parameters[0].Name)
	})

	t.Run("ambiguous name", func(t *testing.T) {
		ambiguous := &openapi3.T{
			OpenAPI: "3.0.3",
			Info:    &openapi3.Info{Title: "t", Version: "1"},
			Paths: openapi3.NewPaths(
				openapi3.WithPath("/x/{shared}", &openapi3.PathItem{
					Get: &openapi3.Operation{
						OperationID: "amb",
						Parameters: openapi3.Parameters{
							{Value: openapi3.NewPathParameter("shared")},
							{Value: openapi3.NewHeaderParameter("shared")},
						},
					},
				}),
			),
		}
		docs2, derr := link.NewDocuments(map[string]*openapi3.T{"api.yaml": ambiguous})
		require.NoError(t, derr)
		l := &openapi3.Link{
			OperationID: "amb",
			Parameters:  map[string]any{"shared": "v"},
		}
		_, err := link.Evaluate(docs2, link.Source{Document: ambiguous}, l, createExchange())
		var ue *link.UnassignableError
		require.ErrorAs(t, err, &ue)
		require.Contains(t, ue.Error(), "ambiguous")
	})
}

func TestRequestBodyOnBodylessTarget(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	l := &openapi3.Link{
		OperationID: "getWidget",
		RequestBody: map[string]any{"x": 1},
	}
	_, err = link.Evaluate(docs, link.Source{Document: doc}, l, createExchange())
	var ue *link.UnassignableError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, "link.requestBody", ue.At.String())
}

func TestCaseInsensitiveResponseHeader(t *testing.T) {
	ex := createExchange()
	ex.Response.Header = http.Header{"X-NEXT-RETRY": {"7"}}
	v, null, err := link.EvaluateValue("$response.header.x-next-retry", ex)
	require.NoError(t, err)
	require.False(t, null)
	require.Equal(t, "7", v)
}

func TestNoTargetSpecified(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	_, err = link.Evaluate(docs, link.Source{Document: doc}, &openapi3.Link{}, createExchange())
	require.Error(t, err)

	// Ensure the typed sentinels are usable with errors.Is-style unwrapping
	// through errors.Join.
	l := &openapi3.Link{
		OperationID: "getWidget",
		Parameters: map[string]any{
			"widgetId": "$response.body#/missing",
			"expanded": "$response.body#/also-missing",
		},
	}
	_, err = link.Evaluate(docs, link.Source{Document: doc}, l, createExchange())
	var me *link.MissingError
	require.True(t, errors.As(err, &me))
}
