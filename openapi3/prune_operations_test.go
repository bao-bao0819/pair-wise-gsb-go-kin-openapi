package openapi3_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

func TestPruneOperationsSelfContainedWithTwoNodes(t *testing.T) {
	ctx := t.Context()
	documents := map[string][]byte{
		"https://example.test/openapi.json": []byte(`{
			"openapi": "3.0.3",
			"info": {"title": "graph", "version": "1.0"},
			"servers": [{"url": "https://api.example.test"}],
			"security": [{"shared": []}],
			"paths": {
				"/nodes/{nodeId}": {
					"parameters": [
						{"$ref": "common.json#/components/parameters/NodeId"},
						{"name":"trace","in":"header","schema":{"type":"string"}}
					],
					"get": {
						"operationId": "getNode",
						"servers": [{"url": "https://node.example.test"}],
						"responses": {
							"200": {
								"description": "ok",
								"content": {"application/json": {"schema": {"$ref": "common.json#/components/schemas/Node"}}},
								"links": {
									"selected": {"operationId": "createNode"},
									"omitted": {"operationId": "deleteNode"},
									"external": {"operationRef": "common.json#/paths/~1external-nodes/post"}
								}
							}
						},
						"callbacks": {
							"changed": {
								"$ref": "common.json#/components/callbacks/NodeChanged"
							}
						}
					},
					"delete": {"operationId": "deleteNode", "responses": {"204": {"description": "deleted"}}},
					"post": {
						"operationId": "createNode",
						"requestBody": {"$ref": "#/components/requestBodies/NodeCreate"},
						"responses": {"201": {"$ref": "common.json#/components/responses/NodeCreated"}}
					}
				}
			},
			"components": {
				"securitySchemes": {"shared": {"type":"http","scheme":"bearer"}},
				"requestBodies": {
					"NodeCreate": {
						"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Node"}}}
					}
				},
				"schemas": {
					"Node": {
						"type": "object",
						"required": ["kind"],
						"discriminator": {"propertyName": "kind", "mapping": {"root": "Node", "common": "common.json#/components/schemas/Node"}},
						"properties": {
							"kind": {"type": "string"},
							"common": {"$ref": "common.json#/components/schemas/Node"}
						}
					},
					"Unreachable": {"type": "string"}
				}
			}
		}`),
		"https://example.test/common.json": []byte(`{
			"openapi": "3.0.3",
			"info": {"title": "common", "version": "1.0"},
			"paths": {
				"/external-nodes": {
					"post": {"operationId": "externalNode", "responses": {"201": {"description": "created"}}}
				}
			},
			"components": {
				"parameters": {
					"NodeId": {"name": "nodeId", "in": "path", "required": true, "schema": {"$ref": "#/components/schemas/NodeId"}}
				},
				"responses": {
					"NodeCreated": {
						"description": "created",
						"headers": {"X-Node": {"$ref": "#/components/headers/NodeHeader"}},
						"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Node"}}},
						"links": {"next": {"operationId": "externalNode"}}
					}
				},
				"headers": {
					"NodeHeader": {"schema": {"$ref": "#/components/schemas/Node"}}
				},
				"callbacks": {
					"NodeChanged": {
						"https://example.test/hooks/{nodeId}": {
							"post": {
								"operationId": "nodeChangedCallback",
								"requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Node"}}}},
								"responses": {"200": {"description": "ok"}}
							}
						}
					}
				},
				"schemas": {
					"a/b~c": {"type": "string"},
					"NodeId": {"type": "string"},
					"Node": {
						"type": "object",
						"properties": {
							"id": {"type": "string"},
							"escaped": {"$ref": "#/components/schemas/a~1b~0c"},
							"child": {"$ref": "#/components/schemas/Node"}
						}
					},
					"Unreachable": {"type": "integer"}
				}
			}
		}`),
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
		data, ok := documents[location.String()]
		if !ok {
			return nil, errors.New("unexpected URI " + location.String())
		}
		return data, nil
	}
	rootURL, err := url.Parse("https://example.test/openapi.json")
	require.NoError(t, err)
	doc, err := loader.LoadFromURI(rootURL)
	require.NoError(t, err)

	result, err := openapi3.PruneOperations(t.Context(), doc, []string{"getNode", "createNode"}, loader)
	require.NoError(t, err)

	data, err := json.Marshal(result.Document)
	require.NoError(t, err)

	jsonText := string(data)
	require.NotContains(t, jsonText, "common.json")
	require.NotContains(t, jsonText, "deleteNode")
	require.NotContains(t, jsonText, "Unreachable")
	require.Contains(t, jsonText, "common_Node")
	require.Len(t, result.Document.Paths.Map(), 1)
	require.Contains(t, result.Document.Components.Schemas, "Node")
	require.Contains(t, result.Document.Components.Schemas, "common_Node")
	require.Contains(t, result.Document.Components.Schemas, "common_a_b_c")
	require.Equal(t, []openapi3.PrunedLink{
		{Name: "external", Source: "https://example.test/openapi.json#/paths/~1nodes~1{nodeId}/get/responses/200/links/external", OperationRef: "common.json#/paths/~1external-nodes/post"},
		{Name: "next", Source: "https://example.test/common.json#/links/next", OperationID: "externalNode"},
		{Name: "omitted", Source: "https://example.test/openapi.json#/paths/~1nodes~1{nodeId}/get/responses/200/links/omitted", OperationID: "deleteNode"},
	}, result.PrunedLinks)

	sealedLoader := openapi3.NewLoader()
	sealedLoader.ReadFromURIFunc = func(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
		t.Fatalf("external reads are forbidden: %s", location)
		return nil, nil
	}
	sealedDoc, err := sealedLoader.LoadFromDataWithPath(data, &url.URL{Path: "pruned.json"})
	require.NoError(t, err)
	require.NoError(t, sealedDoc.Validate(ctx))
	t.Logf("pruned=%s", data)

	router, err := gorillamux.NewRouter(sealedDoc)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, "https://node.example.test/nodes/42", bytes.NewReader([]byte(`{"kind":"common","id":"42","child":{"id":"43"}}`)))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	route, pathParams, err := router.FindRoute(request)
	require.NoError(t, err)
	require.Equal(t, "42", pathParams["nodeId"])
	require.NoError(t, openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{
		Request:    request,
		PathParams: pathParams,
		Route:      route,
		Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}))

	require.Contains(t, doc.Paths.Value("/nodes/{nodeId}").Get.Responses.Value("200").Value.Links, "omitted")
}
