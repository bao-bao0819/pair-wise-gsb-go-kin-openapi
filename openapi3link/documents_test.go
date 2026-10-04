package openapi3link_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3link"
)

func opRefDoc() *openapi3.T {
	idParam := &openapi3.ParameterRef{Value: openapi3.NewPathParameter("id").
		WithSchema(openapi3.NewStringSchema())}
	getUser := &openapi3.Operation{
		OperationID: "getUserByRef",
		Parameters:  openapi3.Parameters{idParam},
		Responses:   newJSONResponses(openapi3.NewObjectSchema()),
	}

	bodySchema := openapi3.NewObjectSchema()
	bodySchema.Properties = openapi3.Schemas{
		"id":   &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
		"name": &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
	}
	createUser := &openapi3.Operation{
		OperationID: "createUserByRef",
		Responses:   &openapi3.Responses{},
	}
	createUser.AddResponse(201, &openapi3.Response{
		Description: strPtr("created"),
		Content:     openapi3.NewContentWithJSONSchema(bodySchema),
		Links: openapi3.Links{
			"ByPointer": &openapi3.LinkRef{Value: &openapi3.Link{
				OperationRef: "#/paths/~1users~1{id}/get",
				Parameters:   map[string]any{"id": "$response.body#/id"},
			}},
			"ByPointerMissingPath": &openapi3.LinkRef{Value: &openapi3.Link{
				OperationRef: "#/paths/~1nope/get",
				Parameters:   map[string]any{"id": "$response.body#/id"},
			}},
			"ByPointerMissingMethod": &openapi3.LinkRef{Value: &openapi3.Link{
				OperationRef: "#/paths/~1users~1{id}/delete",
				Parameters:   map[string]any{"id": "$response.body#/id"},
			}},
		},
	})

	return &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "ref", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/users/{id}", &openapi3.PathItem{Get: getUser}),
			openapi3.WithPath("/users", &openapi3.PathItem{Post: createUser}),
		),
	}
}

func TestOperationRefResolution(t *testing.T) {
	doc := opRefDoc()
	docs, err := openapi3link.NewDocuments(openapi3link.Document{URI: "main.yaml", T: doc})
	require.NoError(t, err)

	exchange := &openapi3link.Exchange{
		Response: openapi3link.ResponseData{
			StatusCode: 201,
			Body:       map[string]any{"id": "u-9"},
		},
	}

	links := doc.Paths.Value("/users").Post.Responses.Value("201").Value.Links

	t.Run("valid pointer", func(t *testing.T) {
		plan, err := docs.Evaluate(links["ByPointer"].Value, exchange, "main.yaml")
		require.NoError(t, err)
		assert.Equal(t, "GET", plan.Method)
		assert.Equal(t, "/users/{id}", plan.Path)
		assert.Equal(t, "u-9", plan.Parameter(openapi3.ParameterInPath, "id").Value)
	})

	t.Run("missing path", func(t *testing.T) {
		_, err := docs.Evaluate(links["ByPointerMissingPath"].Value, exchange, "main.yaml")
		require.Error(t, err)
		var refErr *openapi3link.OperationRefError
		require.True(t, errors.As(err, &refErr))
		assert.Contains(t, refErr.Error(), `"#/paths/~1nope/get"`)
		assert.Contains(t, refErr.Error(), "does not exist")
	})

	t.Run("missing method", func(t *testing.T) {
		_, err := docs.Evaluate(links["ByPointerMissingMethod"].Value, exchange, "main.yaml")
		require.Error(t, err)
		var refErr *openapi3link.OperationRefError
		assert.True(t, errors.As(err, &refErr))
	})

	t.Run("unknown document URI", func(t *testing.T) {
		link := &openapi3.Link{OperationRef: "other.yaml#/paths/~1users/get"}
		_, err := docs.Evaluate(link, exchange, "main.yaml")
		require.Error(t, err)
		var refErr *openapi3link.OperationRefError
		require.True(t, errors.As(err, &refErr))
		assert.Contains(t, refErr.Error(), "not part of the provided documents")
	})
}

func TestOperationRefAcrossDocuments(t *testing.T) {
	other := opRefDoc()
	// Rename so the operationId index remains unique across docs.
	other.Paths.Value("/users/{id}").Get.OperationID = "getUserFromOther"

	docs, err := openapi3link.NewDocuments(
		openapi3link.Document{URI: "main.yaml", T: buildCreateThenGetDocument()},
		openapi3link.Document{URI: "other.yaml", T: other},
	)
	require.NoError(t, err)

	link := &openapi3.Link{
		OperationRef: "other.yaml#/paths/~1users~1{id}/get",
		Parameters:   map[string]any{"id": "$response.body#/id"},
	}
	exchange := &openapi3link.Exchange{
		Response: openapi3link.ResponseData{StatusCode: 201, Body: map[string]any{"id": "u-10"}},
	}
	plan, err := docs.Evaluate(link, exchange, "main.yaml")
	require.NoError(t, err)
	assert.Equal(t, "/users/{id}", plan.Path)
	assert.Equal(t, "u-10", plan.Parameter(openapi3.ParameterInPath, "id").Value)
}

func TestDuplicateOperationID(t *testing.T) {
	docA := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "a", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/a", &openapi3.PathItem{Get: &openapi3.Operation{
				OperationID: "dup",
				Responses:   newJSONResponses(openapi3.NewObjectSchema()),
			}}),
		),
	}
	docB := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "b", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/b", &openapi3.PathItem{Post: &openapi3.Operation{
				OperationID: "dup",
				Responses:   newJSONResponses(openapi3.NewObjectSchema()),
			}}),
		),
	}

	_, err := openapi3link.NewDocuments(
		openapi3link.Document{URI: "a.yaml", T: docA},
		openapi3link.Document{URI: "b.yaml", T: docB},
	)
	require.Error(t, err)
	var dupErr *openapi3link.DuplicateOperationIDError
	require.True(t, errors.As(err, &dupErr))
	assert.Equal(t, "dup", dupErr.OperationID)
	assert.Contains(t, err.Error(), "a.yaml#/a/get")
	assert.Contains(t, err.Error(), "b.yaml#/b/post")
}

func TestOperationIDNotFound(t *testing.T) {
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: expressionDoc()})
	require.NoError(t, err)

	link := &openapi3.Link{OperationID: "does-not-exist"}
	_, err = docs.Evaluate(link, &openapi3link.Exchange{}, "")
	require.Error(t, err)
	var notFound *openapi3link.OperationNotFoundError
	assert.True(t, errors.As(err, &notFound))
}

func TestRequestBodyPlan(t *testing.T) {
	// POST /echo accepts a JSON body and a POST /trigger response links to it.
	echoSchema := openapi3.NewObjectSchema()
	echoSchema.Properties = openapi3.Schemas{
		"id":   &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
		"name": &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
	}
	echo := &openapi3.Operation{
		OperationID: "echo",
		RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{
			Required: true,
			Content:  openapi3.NewContentWithJSONSchema(echoSchema),
		}},
		Responses: newJSONResponses(echoSchema),
	}
	trigger := &openapi3.Operation{
		OperationID: "trigger",
		Responses:   &openapi3.Responses{},
	}
	trigger.AddResponse(200, &openapi3.Response{
		Links: openapi3.Links{
			"Echo": &openapi3.LinkRef{Value: &openapi3.Link{
				OperationID: "echo",
				RequestBody: "$response.body",
			}},
		},
	})
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "echo", Version: "1"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/echo", &openapi3.PathItem{Post: echo}),
			openapi3.WithPath("/trigger", &openapi3.PathItem{Post: trigger}),
		),
	}
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	exchange := &openapi3link.Exchange{
		Response: openapi3link.ResponseData{
			StatusCode: 200,
			Body:       map[string]any{"id": "u-1", "name": "alice"},
		},
	}
	link := doc.Paths.Value("/trigger").Post.Responses.Value("200").Value.Links["Echo"].Value

	plan, err := docs.Evaluate(link, exchange, "")
	require.NoError(t, err)
	require.NotNil(t, plan.RequestBody)
	assert.Equal(t, map[string]any{"id": "u-1", "name": "alice"}, plan.RequestBody.Value)
	assert.False(t, plan.RequestBody.IsNull)
	assert.True(t, plan.RequestBody.RequestBody.Required)
}

func TestRequestBodyNotDeclared(t *testing.T) {
	doc := expressionDoc() // GET operation, no requestBody
	docs, err := openapi3link.NewDocuments(openapi3link.Document{T: doc})
	require.NoError(t, err)

	link := &openapi3.Link{OperationID: "sink", RequestBody: "$response.body"}
	exchange := &openapi3link.Exchange{Response: openapi3link.ResponseData{Body: map[string]any{"x": 1}}}
	_, err = docs.Evaluate(link, exchange, "")
	require.Error(t, err)
	var notDeclared *openapi3link.RequestBodyNotDeclaredError
	assert.True(t, errors.As(err, &notDeclared))
}
