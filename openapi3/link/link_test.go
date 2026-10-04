package link_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3/link"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createThenGetDoc models a create-resource operation linking to a
// get-resource operation, entirely in memory.
func createThenGetDoc() *openapi3.T {
	return &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "t", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/widgets", &openapi3.PathItem{
				Post: &openapi3.Operation{
					OperationID: "createWidget",
					Responses: openapi3.NewResponses(
						openapi3.WithStatus(201, &openapi3.ResponseRef{Value: &openapi3.Response{
							Description: strPtr("created"),
							Content: openapi3.Content{
								"application/json": &openapi3.MediaType{
									Schema: &openapi3.SchemaRef{Value: openapi3.NewObjectSchema()},
								},
							},
							Links: openapi3.Links{
								"get": &openapi3.LinkRef{Value: &openapi3.Link{
									OperationID: "getWidget",
									Parameters: map[string]any{
										"widgetId": "$response.body#/id",
										"expanded": true,
									},
								}},
							},
						}}),
					),
				},
			}),
			openapi3.WithPath("/widgets/{widgetId}", &openapi3.PathItem{
				Parameters: openapi3.Parameters{
					{Value: openapi3.NewPathParameter("widgetId")},
				},
				Get: &openapi3.Operation{
					OperationID: "getWidget",
					Parameters: openapi3.Parameters{
						{Value: openapi3.NewQueryParameter("expanded")},
					},
				},
			}),
		),
	}
}

func strPtr(s string) *string { return &s }

func createExchange() *link.Exchange {
	u, _ := url.Parse("https://api.example.com/widgets?verbose=1&tag=a&tag=b")
	return &link.Exchange{
		Request: &link.Request{
			Method: http.MethodPost,
			URL:    u,
			Header: http.Header{"X-Trace": {"req-trace"}},
			Path:   map[string]any{},
			Body:   map[string]any{"name": "gear"},
		},
		Response: &link.Response{
			StatusCode: http.StatusCreated,
			Header: http.Header{
				"X-Next-Retry": {"42"},
				"Location":     {"/widgets/w-7"},
			},
			Body: map[string]any{
				"id":  "w-7",
				"a/b": 4,
				"meta": map[string]any{
					"note": nil,
				},
				"count": 3,
			},
		},
	}
}

func findLink(doc *openapi3.T, name string) *openapi3.Link {
	resp := doc.Paths.Value("/widgets").Post.Responses.Value("201").Value
	return resp.Links[name].Value
}

func TestEvaluateCreateThenGet(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	plan, err := link.Evaluate(docs, link.Source{Document: doc, DocURL: "api.yaml"},
		findLink(doc, "get"), createExchange())
	require.NoError(t, err)

	assert.Equal(t, "GET", plan.Target.Method)
	assert.Equal(t, "/widgets/{widgetId}", plan.Target.Path)

	// The path template is handed over untouched; the value is typed, not
	// spliced into the URL.
	byID := map[string]link.ParamValue{}
	for _, p := range plan.Parameters {
		byID[p.In+":"+p.Name] = p
	}
	require.Contains(t, byID, "path:widgetId")
	require.Contains(t, byID, "query:expanded")
	assert.Equal(t, "w-7", byID["path:widgetId"].Value)
	assert.Equal(t, true, byID["query:expanded"].Value)
	assert.False(t, byID["path:widgetId"].Null)
	assert.False(t, plan.HasBody)
}

func TestEvaluateBuiltinAndRequestExpressions(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{"$url", "https://api.example.com/widgets?verbose=1&tag=a&tag=b"},
		{"$method", "POST"},
		{"$statusCode", 201},
		{"$request.query.verbose", "1"},
		{"$request.query.tag", []string{"a", "b"}},
		{"$request.header.x-trace", "req-trace"},
		{"$request.body#/name", "gear"},
		{"$response.header.x-next-retry", "42"},
		{"$response.body#/count", 3},
		{"$response.body#/a~1b", 4},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, null, err := link.EvaluateValue(tc.expr, createExchange())
			require.NoError(t, err)
			assert.False(t, null)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEvaluateExplicitNull(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	l := &openapi3.Link{
		OperationID: "getWidget",
		Parameters:  map[string]any{"widgetId": "$response.body#/meta/note"},
	}
	plan, err := link.Evaluate(docs, link.Source{Document: doc}, l, createExchange())
	require.NoError(t, err)
	require.Len(t, plan.Parameters, 1)
	assert.True(t, plan.Parameters[0].Null)
	assert.Nil(t, plan.Parameters[0].Value)
}

func TestOperationRefFragment(t *testing.T) {
	doc := createThenGetDoc()
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	l := &openapi3.Link{
		OperationRef: "#/paths/~1widgets~1{widgetId}/get",
		Parameters:   map[string]any{"widgetId": "$response.body#/id"},
	}
	plan, err := link.Evaluate(docs, link.Source{Document: doc, DocURL: "api.yaml"}, l, createExchange())
	require.NoError(t, err)
	assert.Equal(t, "getWidget", plan.Target.Operation.OperationID)
}

func TestOperationRefExternalDocument(t *testing.T) {
	main := createThenGetDoc()
	other := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "o", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/things/{id}", &openapi3.PathItem{
				Get: &openapi3.Operation{
					OperationID: "getThing",
					Parameters:  openapi3.Parameters{{Value: openapi3.NewPathParameter("id")}},
				},
			}),
		),
	}
	docs, err := link.NewDocuments(map[string]*openapi3.T{
		"dir/api.yaml":   main,
		"dir/other.yaml": other,
	})
	require.NoError(t, err)

	l := &openapi3.Link{
		OperationRef: "other.yaml#/paths/~1things~1{id}/get",
		Parameters:   map[string]any{"id": "$response.body#/id"},
	}
	plan, err := link.Evaluate(docs, link.Source{Document: main, DocURL: "dir/api.yaml"}, l, createExchange())
	require.NoError(t, err)
	assert.Equal(t, "getThing", plan.Target.Operation.OperationID)
	assert.Equal(t, "/things/{id}", plan.Target.Path)
	assert.Equal(t, "dir/other.yaml", plan.Target.DocURL)
}

func TestEvaluateRequestBody(t *testing.T) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "t", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/widgets", &openapi3.PathItem{
				Post: &openapi3.Operation{
					OperationID: "createWidget",
					Responses: openapi3.NewResponses(
						openapi3.WithName("201", openapi3.NewResponse().WithDescription("created")),
					),
				},
			}),
			openapi3.WithPath("/widgets/{id}/clone", &openapi3.PathItem{
				Post: &openapi3.Operation{
					OperationID: "cloneWidget",
					Parameters: openapi3.Parameters{
						{Value: openapi3.NewPathParameter("id")},
					},
					RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{
						Content: openapi3.Content{"application/json": {}},
					}},
				},
			}),
		),
	}
	docs, err := link.NewDocuments(map[string]*openapi3.T{"api.yaml": doc})
	require.NoError(t, err)

	l := &openapi3.Link{
		OperationID: "cloneWidget",
		Parameters: map[string]any{
			"id": "$response.body#/id",
		},
		RequestBody: map[string]any{
			"name":  "$response.body#/name",
			"count": 7,
		},
	}
	resp := &link.Response{StatusCode: 201, Body: map[string]any{"id": "w-7", "name": "gear"}}
	plan, err := link.Evaluate(docs, link.Source{Document: doc}, l, &link.Exchange{Response: resp})
	require.NoError(t, err)
	assert.True(t, plan.HasBody)
	assert.Equal(t, map[string]any{"name": "gear", "count": 7}, plan.Body)
}
