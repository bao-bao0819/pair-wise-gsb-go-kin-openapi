package openapi3_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// pruneMainYAML and pruneNodesYAML are two documents that each define a
// "Node" schema; the one in nodes.yaml is self-referencing.
const pruneMainYAML = `
openapi: 3.0.3
info: {title: node api, version: 1.0.0}
servers:
  - url: https://api.example.com
security:
  - apiKey: []
tags:
  - {name: nodes, description: kept}
  - {name: unused, description: dropped}
paths:
  /nodes:
    parameters:
      - $ref: '#/components/parameters/tenant'
    get:
      operationId: listNodes
      tags: [nodes]
      security:
        - oauth: [read]
      parameters:
        - $ref: './nodes.yaml#/components/parameters/limit'
      responses:
        '200':
          description: page of nodes
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/NodePage'
    post:
      operationId: createNode
      requestBody:
        content:
          application/json:
            schema:
              $ref: './nodes.yaml#/components/schemas/Node'
      responses:
        '201':
          description: created
          content:
            application/json:
              schema:
                $ref: './nodes.yaml#/components/schemas/Node'
          links:
            getNode:
              operationId: getNode
              parameters: {id: '$response.body#/id'}
            deleteNode:
              operationId: deleteNode
  /nodes/{id}:
    get:
      operationId: getNode
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses:
        '200':
          description: a node
          content:
            application/json:
              schema:
                $ref: './nodes.yaml#/components/schemas/Node'
    delete:
      operationId: deleteNode
      responses:
        '204': {description: gone}
components:
  parameters:
    tenant:
      name: x-tenant
      in: header
      schema: {type: string}
    unusedParam:
      name: x-unused
      in: query
      schema: {type: string}
  schemas:
    Node:
      type: object
      properties:
        name: {type: string}
    NodePage:
      type: object
      properties:
        items:
          type: array
          items:
            $ref: '#/components/schemas/Node'
        external:
          $ref: './nodes.yaml#/components/schemas/Node'
    Unused:
      type: object
  securitySchemes:
    apiKey: {type: apiKey, in: header, name: x-api-key}
    oauth:
      type: oauth2
      flows:
        clientCredentials:
          tokenUrl: https://auth.example.com/token
          scopes: {read: read nodes}
    unusedScheme: {type: http, scheme: basic}
`

const pruneNodesYAML = `
components:
  parameters:
    limit:
      name: limit
      in: query
      schema: {type: integer}
  schemas:
    Node:
      type: object
      properties:
        id: {type: string}
        children:
          type: array
          items:
            $ref: '#/components/schemas/Node'
`

func memoryLoader(t *testing.T, files map[string]string) *openapi3.Loader {
	t.Helper()
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
		data, ok := files[location.String()]
		if !ok {
			return nil, errors.New("unknown URI: " + location.String())
		}
		return []byte(data), nil
	}
	return loader
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func loadPruneFixture(t *testing.T) (*openapi3.T, *openapi3.Loader) {
	t.Helper()
	loader := memoryLoader(t, map[string]string{
		"mem://spec/main.yaml":  pruneMainYAML,
		"mem://spec/nodes.yaml": pruneNodesYAML,
	})
	doc, err := loader.LoadFromURI(mustParseURL(t, "mem://spec/main.yaml"))
	require.NoError(t, err)
	return doc, loader
}

func TestPruneOperations(t *testing.T) {
	doc, loader := loadPruneFixture(t)

	pruned, report, err := openapi3.PruneOperations(doc, loader, []string{"listNodes", "createNode", "getNode"})
	require.NoError(t, err)
	require.NotNil(t, pruned)

	// Only the selected operations survive.
	nodes := pruned.Paths.Map()["/nodes"]
	require.NotNil(t, nodes)
	require.NotNil(t, nodes.Get)
	require.NotNil(t, nodes.Post)
	nodeByID := pruned.Paths.Map()["/nodes/{id}"]
	require.NotNil(t, nodeByID)
	require.NotNil(t, nodeByID.Get)
	require.Nil(t, nodeByID.Delete, "unselected operation must be dropped")

	// Path-level parameters are preserved.
	require.Len(t, nodes.Parameters, 1)
	require.Equal(t, "#/components/parameters/tenant", nodes.Parameters[0].Ref)

	// Effective security and servers are preserved at every level.
	require.Equal(t, "https://api.example.com", pruned.Servers[0].URL)
	require.Len(t, pruned.Security, 1)
	require.Contains(t, pruned.Security[0], "apiKey")
	require.NotNil(t, pruned.Components.SecuritySchemes["apiKey"])
	require.NotNil(t, pruned.Components.SecuritySchemes["oauth"])
	require.Nil(t, pruned.Components.SecuritySchemes["unusedScheme"])

	// The two "Node" components from different files must not overwrite
	// each other.
	require.Len(t, pruned.Components.Schemas, 3, "NodePage plus the two Nodes")
	require.Nil(t, pruned.Components.Schemas["Unused"], "unreachable component must be dropped")
	require.Nil(t, pruned.Components.Parameters["unusedParam"])
	require.NotNil(t, pruned.Components.Parameters["limit"])

	// Identify the two Nodes by shape, whatever names they received.
	var selfRefName, leafName string
	for name, schemaRef := range pruned.Components.Schemas {
		if name == "NodePage" {
			continue
		}
		if _, ok := schemaRef.Value.Properties["children"]; ok {
			selfRefName = name
		} else {
			leafName = name
		}
	}
	require.NotEmpty(t, selfRefName)
	require.NotEmpty(t, leafName)
	require.NotEqual(t, selfRefName, leafName)

	// The self-reference inside the external Node points at its own new name.
	selfRef := pruned.Components.Schemas[selfRefName].Value
	children := selfRef.Properties["children"].Value
	require.Equal(t, "#/components/schemas/"+selfRefName, children.Items.Ref)

	// NodePage's two references point at the right Node each.
	nodePage := pruned.Components.Schemas["NodePage"].Value
	require.Equal(t, "#/components/schemas/"+leafName, nodePage.Properties["items"].Value.Items.Ref)
	require.Equal(t, "#/components/schemas/"+selfRefName, nodePage.Properties["external"].Ref)

	// The link to the unselected deleteNode is gone and reported; the link
	// to the selected getNode stays.
	links := nodes.Post.Responses.Map()["201"].Value.Links
	require.Contains(t, links, "getNode")
	require.NotContains(t, links, "deleteNode")
	require.Len(t, report.RemovedLinks, 1)
	require.Equal(t, "deleteNode", report.RemovedLinks[0].Name)
	require.Equal(t, "deleteNode", report.RemovedLinks[0].OperationID)
	require.Contains(t, report.RemovedLinks[0].Source, "paths./nodes/post")
	require.Contains(t, report.RemovedLinks[0].Source, "links.deleteNode")

	// A rename happened and is reported.
	require.Len(t, report.RenamedComponents, 1)
	require.Equal(t, "schemas", report.RenamedComponents[0].Collection)

	// Only the used tag survives.
	require.Len(t, pruned.Tags, 1)
	require.Equal(t, "nodes", pruned.Tags[0].Name)

	// The original document is untouched.
	require.NotNil(t, doc.Paths.Map()["/nodes/{id}"].Delete)
	require.NotNil(t, doc.Components.Schemas["Unused"])
	require.Contains(t, doc.Paths.Map()["/nodes"].Post.Responses.Map()["201"].Value.Links, "deleteNode")

	// The pruned document serializes without any external reference.
	data, err := json.Marshal(pruned)
	require.NoError(t, err)
	require.NotContains(t, string(data), "nodes.yaml")
	require.NotContains(t, string(data), "mem://")

	// Reload it with a loader that forbids any external read.
	standaloneLoader := openapi3.NewLoader()
	standaloneLoader.ReadFromURIFunc = func(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
		return nil, errors.New("external reads are forbidden: " + location.String())
	}
	standalone, err := standaloneLoader.LoadFromData(data)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, standalone.Validate(ctx))

	// Validate a request against the reloaded document, exercising the
	// self-referencing schema through the request body.
	router, err := legacy.NewRouter(standalone)
	require.NoError(t, err)

	body := `{"id": "1", "children": [{"id": "2", "children": []}]}`
	req := httptest.NewRequest("POST", "https://api.example.com/nodes", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "secret")
	route, pathParams, err := router.FindRoute(req)
	require.NoError(t, err)
	require.NoError(t, openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}))

	// A body that violates the recursive schema must be rejected.
	badReq := httptest.NewRequest("POST", "https://api.example.com/nodes", strings.NewReader(`{"id": 42}`))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("x-api-key", "secret")
	badRoute, badPathParams, err := router.FindRoute(badReq)
	require.NoError(t, err)
	require.Error(t, openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{
		Request:    badReq,
		PathParams: badPathParams,
		Route:      badRoute,
		Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}))
}

func TestPruneOperations_CallbacksAndDiscriminator(t *testing.T) {
	files := map[string]string{
		"mem://cb/main.yaml": `
openapi: 3.0.3
info: {title: callbacks, version: 1.0.0}
paths:
  /subscribe:
    post:
      operationId: subscribe
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                callbackUrl: {type: string}
      callbacks:
        onEvent:
          '{$request.body#/callbackUrl}':
            post:
              requestBody:
                content:
                  application/json:
                    schema:
                      $ref: './ext.yaml#/components/schemas/Event'
              responses:
                '200': {description: ok}
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Result'
components:
  schemas:
    Result:
      type: object
      discriminator:
        propertyName: kind
        mapping:
          event: './ext.yaml#/components/schemas/Event'
      oneOf:
        - $ref: '#/components/schemas/ResultA'
        - $ref: './ext.yaml#/components/schemas/Event'
    ResultA:
      type: object
      properties:
        kind: {type: string}
`,
		"mem://cb/ext.yaml": `
components:
  schemas:
    Event:
      type: object
      properties:
        kind: {type: string}
        payload: {type: string}
`,
	}
	loader := memoryLoader(t, files)
	doc, err := loader.LoadFromURI(mustParseURL(t, "mem://cb/main.yaml"))
	require.NoError(t, err)

	pruned, _, err := openapi3.PruneOperations(doc, loader, []string{"subscribe"})
	require.NoError(t, err)

	// The callback and its external schema survive.
	callback := pruned.Paths.Map()["/subscribe"].Post.Callbacks["onEvent"].Value
	callbackOp := callback.Map()["{$request.body#/callbackUrl}"].Post
	eventRef := callbackOp.RequestBody.Value.Content["application/json"].Schema.Ref
	require.Equal(t, "#/components/schemas/Event", eventRef)
	require.NotNil(t, pruned.Components.Schemas["Event"])

	// The discriminator mapping is rewritten to the same internal reference
	// as the oneOf member it selects.
	result := pruned.Components.Schemas["Result"].Value
	require.Equal(t, "#/components/schemas/Event", result.Discriminator.Mapping["event"].Ref)
	require.Equal(t, "#/components/schemas/Event", result.OneOf[1].Ref)
	require.Equal(t, "#/components/schemas/ResultA", result.OneOf[0].Ref)

	data, err := json.Marshal(pruned)
	require.NoError(t, err)
	require.NotContains(t, string(data), "ext.yaml")

	standalone, err := openapi3.NewLoader().LoadFromData(data)
	require.NoError(t, err)
	require.NoError(t, standalone.Validate(context.Background()))
}

func TestPruneOperations_UnknownOperationID(t *testing.T) {
	doc, loader := loadPruneFixture(t)
	_, _, err := openapi3.PruneOperations(doc, loader, []string{"getNode", "noSuchOperation"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "noSuchOperation")
}

func TestPruneOperations_UnresolvedRefReportsChain(t *testing.T) {
	doc, loader := loadPruneFixture(t)

	// Break a reference after loading: the target can no longer be copied.
	doc.Components.Schemas["NodePage"].Value.Properties["external"].Value = nil

	_, _, err := openapi3.PruneOperations(doc, loader, []string{"listNodes"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unresolved reference")
	// The error carries the full chain from the operation to the broken ref.
	require.Contains(t, err.Error(), "paths./nodes/get")
	require.Contains(t, err.Error(), "NodePage")
}
