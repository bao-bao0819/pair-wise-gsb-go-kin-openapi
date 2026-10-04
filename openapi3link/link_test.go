package openapi3link_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3link"
)

// buildCreateThenGetDocument builds the in-memory example used across the
// tests: POST /users creates a resource and its 201 response links to
// GET /users/{id} (operationId "getUser").
func buildCreateThenGetDocument() *openapi3.T {
	idParam := &openapi3.ParameterRef{Value: openapi3.NewPathParameter("id").
		WithSchema(openapi3.NewStringSchema())}
	verboseParam := &openapi3.ParameterRef{Value: openapi3.NewQueryParameter("verbose").
		WithSchema(openapi3.NewBoolSchema())}

	createBodySchema := openapi3.NewObjectSchema()
	createBodySchema.Properties = openapi3.Schemas{
		"name": &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
	}

	createResponseBody := openapi3.NewObjectSchema()
	createResponseBody.Properties = openapi3.Schemas{
		"id":   &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
		"name": &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
	}

	getUser := &openapi3.Operation{
		OperationID: "getUser",
		Parameters:  openapi3.Parameters{idParam, verboseParam},
		Responses:   newJSONResponses(openapi3.NewObjectSchema()),
	}

	createUser := &openapi3.Operation{
		OperationID: "createUser",
		RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{
			Required: true,
			Content:  openapi3.NewContentWithJSONSchema(createBodySchema),
		}},
		Responses: &openapi3.Responses{},
	}
	createUser.AddResponse(201, &openapi3.Response{
		Description: strPtr("created"),
		Content:     openapi3.NewContentWithJSONSchema(createResponseBody),
		Links: openapi3.Links{
			"GetUser": &openapi3.LinkRef{Value: &openapi3.Link{
				OperationID: "getUser",
				Parameters: map[string]any{
					"id":                "$response.body#/id",
					"verbose":           true,
					"header.X-Trace-Id": "$request.header.X-Trace-Id",
				},
			}},
		},
	})

	traceHeader := &openapi3.ParameterRef{Value: openapi3.NewHeaderParameter("X-Trace-Id").
		WithSchema(openapi3.NewStringSchema())}
	getUserWithHeader := *getUser
	getUserWithHeader.Parameters = append(append(openapi3.Parameters{}, getUser.Parameters...), traceHeader)

	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "users", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/users", &openapi3.PathItem{
				Post: createUser,
			}),
			openapi3.WithPath("/users/{id}", &openapi3.PathItem{
				Get: &getUserWithHeader,
			}),
		),
	}
	return doc
}

func newJSONResponses(schema *openapi3.Schema) *openapi3.Responses {
	responses := &openapi3.Responses{}
	responses.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
		Description: strPtr("ok"),
		Content:     openapi3.NewContentWithJSONSchema(schema),
	}})
	return responses
}

func strPtr(value string) *string { return &value }

func TestEvaluateCreateThenGetLink(t *testing.T) {
	doc := buildCreateThenGetDocument()
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	link := doc.Paths.Value("/users").Post.Responses.Value("201").Value.Links["GetUser"].Value

	requestURL, err := url.Parse("https://api.example.com/users")
	require.NoError(t, err)

	exchange := &openapi3link.Exchange{
		Request: openapi3link.RequestData{
			Method:      "POST",
			URL:         requestURL,
			PathParams:  map[string]string{},
			QueryParams: map[string]any{},
			Headers:     map[string]string{"X-Trace-Id": "abc-123"},
			Body:        map[string]any{"name": "alice"},
		},
		Response: openapi3link.ResponseData{
			StatusCode: 201,
			// Deliberately a different casing than the declared header.
			Headers: map[string]string{"location": "/users/u-42"},
			Body:    map[string]any{"id": "u-42", "name": "alice"},
		},
	}

	plan, err := docs.Evaluate(link, exchange, "")
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.Equal(t, "GET", plan.Method)
	assert.Equal(t, "/users/{id}", plan.Path)
	assert.Equal(t, "getUser", plan.OperationID)

	id := plan.Parameter(openapi3.ParameterInPath, "id")
	require.NotNil(t, id)
	assert.Equal(t, "u-42", id.Value)
	assert.False(t, id.IsNull)

	verbose := plan.Parameter(openapi3.ParameterInQuery, "verbose")
	require.NotNil(t, verbose)
	assert.Equal(t, true, verbose.Value)

	trace := plan.Parameter(openapi3.ParameterInHeader, "X-Trace-Id")
	require.NotNil(t, trace)
	assert.Equal(t, "abc-123", trace.Value)

	assert.Nil(t, plan.RequestBody)
}

// expressionDoc builds a document whose single operation "sink" accepts one
// untyped (schema-less) parameter in every location, so raw expression values
// can be inspected without a schema narrowing them.
func expressionDoc() *openapi3.T {
	op := &openapi3.Operation{
		OperationID: "sink",
		Parameters: openapi3.Parameters{
			{Value: &openapi3.Parameter{Name: "p", In: openapi3.ParameterInPath, Required: true}},
			{Value: &openapi3.Parameter{Name: "p", In: openapi3.ParameterInQuery}},
			{Value: &openapi3.Parameter{Name: "p", In: openapi3.ParameterInHeader}},
		},
		Responses: newJSONResponses(openapi3.NewObjectSchema()),
	}
	return &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "sink", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/sink/{p}", &openapi3.PathItem{Get: op}),
		),
	}
}

func TestExpressionSources(t *testing.T) {
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: expressionDoc()})
	require.NoError(t, err)

	requestURL, err := url.Parse("https://api.example.com/users")
	require.NoError(t, err)
	exchange := &openapi3link.Exchange{
		Request: openapi3link.RequestData{
			Method:      "POST",
			URL:         requestURL,
			PathParams:  map[string]string{"id": "p-1"},
			QueryParams: map[string]any{"q": "search", "multi": []string{"a", "b"}},
			Headers:     map[string]string{"X-Trace-Id": "t-1"},
			Body: map[string]any{
				"nested": map[string]any{"deep": []any{map[string]any{"k": "v"}}},
			},
		},
		Response: openapi3link.ResponseData{
			StatusCode: 201,
			Headers:    map[string]string{"Location": "/x"},
			Body:       map[string]any{"ok": true},
		},
	}

	cases := []struct {
		expr   string
		target string // qualified "in.name"
		want   any
	}{
		{"$url", "query.p", "https://api.example.com/users"},
		{"$method", "query.p", "post"},
		{"$statusCode", "query.p", 201},
		{"$request.path.id", "query.p", "p-1"},
		{"$request.query.q", "query.p", "search"},
		{"$request.query.multi", "query.p", []string{"a", "b"}},
		{"$request.header.x-trace-id", "query.p", "t-1"},
		{"$request.header.X-Trace-Id", "query.p", "t-1"},
		{"$request.body", "query.p", exchange.Request.Body},
		{"$request.body#/nested/deep/0/k", "query.p", "v"},
		{"$response.header.location", "query.p", "/x"},
		{"$response.header.Location", "query.p", "/x"},
		{"$response.body#/ok", "query.p", true},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			link := &openapi3.Link{
				OperationID: "sink",
				Parameters:  map[string]any{tc.target: tc.expr},
			}
			plan, err := docs.Evaluate(link, exchange, "")
			require.NoError(t, err)
			got := plan.Parameter(openapi3.ParameterInQuery, "p")
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got.Value)
		})
	}
}
