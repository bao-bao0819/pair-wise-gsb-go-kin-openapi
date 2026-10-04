package openapi3filter_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

var strictAttributionSpec = []byte(`
openapi: '3.0.3'
info:
  title: t
  version: '1.0.0'
security:
  - apiKeyAuth: []
paths:
  /items:
    parameters:
      - name: pathOnly
        in: query
        schema:
          type: string
    get:
      parameters:
        - name: first
          in: query
          style: form
          explode: true
          schema:
            type: object
            properties:
              id: {type: string}
              name: {type: string}
        - name: second
          in: query
          style: form
          explode: true
          schema:
            type: object
            properties:
              id: {type: string}
              kind: {type: string}
      responses:
        '200': {description: ok}
  /explicit:
    get:
      parameters:
        - name: id
          in: query
          schema: {type: string}
        - name: filter
          in: query
          style: form
          explode: true
          schema:
            type: object
            properties:
              id: {type: string}
              name: {type: string}
      responses:
        '200': {description: ok}
  /deep:
    get:
      parameters:
        - name: f
          in: query
          style: deepObject
          explode: true
          schema:
            type: object
            properties:
              g: {type: string}
              x: {type: string}
        - name: g
          in: query
          style: deepObject
          explode: true
          schema:
            type: object
            properties:
              h: {type: string}
      responses:
        '200': {description: ok}
  /open:
    get:
      parameters:
        - name: a
          in: query
          style: form
          explode: true
          schema:
            type: object
            additionalProperties: {type: string}
        - name: b
          in: query
          style: form
          explode: true
          schema:
            type: object
            additionalProperties: {type: string}
      responses:
        '200': {description: ok}
  /closed:
    get:
      parameters:
        - name: p
          in: query
          style: form
          explode: true
          schema:
            type: object
            properties:
              id: {type: string}
              name: {type: string}
      responses:
        '200': {description: ok}
  /pathparam:
    parameters:
      - name: pathOnly
        in: query
        schema: {type: string}
    get:
      parameters:
        - name: p
          in: query
          style: form
          explode: true
          schema:
            type: object
            properties:
              id: {type: string}
      responses:
        '200': {description: ok}
components:
  securitySchemes:
    apiKeyAuth:
      type: apiKey
      in: header
      name: X-API-Key
`[1:])

func strictSetup(t *testing.T, path, rawURL string, opts *openapi3filter.Options) (*http.Request, *openapi3filter.RequestValidationInput, *atomic.Bool) {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(strictAttributionSpec)
	require.NoError(t, err)
	require.NoError(t, doc.Validate(loader.Context))
	router, err := gorillamux.NewRouter(doc)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	route, pathParams, err := router.FindRoute(req)
	require.NoError(t, err)
	require.Equal(t, path, route.Path)

	var authCalled atomic.Bool
	if opts == nil {
		opts = &openapi3filter.Options{}
	}
	if opts.AuthenticationFunc == nil {
		opts.AuthenticationFunc = func(ctx context.Context, input *openapi3filter.AuthenticationInput) error {
			authCalled.Store(true)
			return nil
		}
	}
	input := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    opts,
	}
	return req, input, &authCalled
}

// Two exploded objects that share the "id" property are a compile-time,
// spec-only conflict: strict mode rejects every request before decoding or
// invoking authentication, and the request URL is untouched.
func TestStrictAttribution_TwoObjectsShareID_CompileError(t *testing.T) {
	rawQuery := "id=1&name=foo&kind=bar"
	req, input, authCalled := strictSetup(t, "/items", "http://example.test/items?"+rawQuery,
		&openapi3filter.Options{StrictQueryParameterAttribution: true})

	err := openapi3filter.ValidateRequest(context.Background(), input)
	var planErr *openapi3filter.ParameterPlanError
	require.ErrorAs(t, err, &planErr)
	require.NotEmpty(t, planErr.Conflicts)
	require.Equal(t, "id", planErr.Conflicts[0].Key)

	require.False(t, authCalled.Load(), "authentication must not run on attribution failure")
	require.Equal(t, rawQuery, req.URL.RawQuery, "original URL must not be rewritten")
}

// An exploded object property colliding with an explicit parameter is also a
// compile-time conflict.
func TestStrictAttribution_ObjectVsExplicitID_CompileError(t *testing.T) {
	rawQuery := "id=1&name=foo"
	req, input, authCalled := strictSetup(t, "/explicit", "http://example.test/explicit?"+rawQuery,
		&openapi3filter.Options{StrictQueryParameterAttribution: true})

	err := openapi3filter.ValidateRequest(context.Background(), input)
	var planErr *openapi3filter.ParameterPlanError
	require.ErrorAs(t, err, &planErr)
	require.NotEmpty(t, planErr.Conflicts)
	require.Equal(t, "id", planErr.Conflicts[0].Key)
	require.False(t, authCalled.Load())
	require.Equal(t, rawQuery, req.URL.RawQuery)
}

// Escaped square brackets in a deepObject key must not let one item be read as
// two parameters; every item is attributed once and validation succeeds.
func TestStrictAttribution_DeepObjectEscapedBrackets(t *testing.T) {
	// "f%5Bg%5D=1" decodes to key "f[g]" (root prop g of f). The brackets are
	// literal after decoding, so it cannot masquerade as "g[h]".
	rawQuery := "f%5Bg%5D=1&g%5Bh%5D=2&f%5Bx%5D=3"
	req, input, authCalled := strictSetup(t, "/deep", "http://example.test/deep?"+rawQuery,
		&openapi3filter.Options{StrictQueryParameterAttribution: true})

	require.NoError(t, openapi3filter.ValidateRequest(context.Background(), input))
	require.True(t, authCalled.Load())
	require.Equal(t, rawQuery, req.URL.RawQuery)
	require.Nil(t, input.QueryAttribution().Ambiguous)
	require.Nil(t, input.QueryAttribution().Unconsumed)

	// Every decoded raw key occurs in exactly one parameter view.
	values, err := url.ParseQuery(rawQuery)
	require.NoError(t, err)
	counts := map[string]int{}
	for _, key := range []string{"f[g]", "g[h]", "f[x]"} {
		require.Contains(t, values, key)
		counts[key]++
	}
	require.Len(t, counts, 3)
}

// Two open (additionalProperties) exploded objects produce an input-dependent
// conflict: the error lists the raw item with both candidates.
func TestStrictAttribution_TwoOpenObjects_AmbiguousAtRequestTime(t *testing.T) {
	rawQuery := "shared=1"
	req, input, authCalled := strictSetup(t, "/open", "http://example.test/open?"+rawQuery,
		&openapi3filter.Options{StrictQueryParameterAttribution: true})

	err := openapi3filter.ValidateRequest(context.Background(), input)
	var attrErr *openapi3filter.QueryAttributionError
	require.ErrorAs(t, err, &attrErr)
	require.Len(t, attrErr.Ambiguous, 1)
	require.Equal(t, "shared=1", attrErr.Ambiguous[0].Raw)
	require.ElementsMatch(t, []string{"a", "b"}, attrErr.Ambiguous[0].Candidates)
	require.False(t, authCalled.Load())
	require.Equal(t, rawQuery, req.URL.RawQuery)
	require.NotNil(t, input.QueryAttribution())
}

// Unconsumed items are reported but do not fail unless rejection is enabled.
func TestStrictAttribution_Unconsumed(t *testing.T) {
	t.Run("reported but allowed", func(t *testing.T) {
		_, in, _ := strictSetup(t, "/closed", "http://example.test/closed?id=1&mystery=2",
			&openapi3filter.Options{StrictQueryParameterAttribution: true})
		err := openapi3filter.ValidateRequest(context.Background(), in)
		require.NoError(t, err)
		report := in.QueryAttribution()
		require.Empty(t, report.Ambiguous)
		require.Len(t, report.Unconsumed, 1)
		require.Equal(t, "mystery=2", report.Unconsumed[0].Raw)
	})

	t.Run("rejected when enabled", func(t *testing.T) {
		req, in, authCalled := strictSetup(t, "/closed", "http://example.test/closed?mystery=2",
			&openapi3filter.Options{
				StrictQueryParameterAttribution: true,
				RejectUnconsumedQueryParams:     true,
			})
		err := openapi3filter.ValidateRequest(context.Background(), in)
		var attrErr *openapi3filter.QueryAttributionError
		require.ErrorAs(t, err, &attrErr)
		require.Len(t, attrErr.Unconsumed, 1)
		require.Equal(t, "mystery=2", attrErr.Unconsumed[0].Raw)
		require.False(t, authCalled.Load())
		require.Equal(t, "mystery=2", req.URL.RawQuery)
	})
}

// The legacy loose path stays permissive: the same shared-id request decodes
// successfully and keeps the original behavior.
func TestLooseAttribution_SharedIDStillDecodes(t *testing.T) {
	rawQuery := "id=1&name=foo&kind=bar"
	req, input, authCalled := strictSetup(t, "/items", "http://example.test/items?"+rawQuery, nil)

	require.NoError(t, openapi3filter.ValidateRequest(context.Background(), input))
	require.True(t, authCalled.Load())
	require.Equal(t, rawQuery, req.URL.RawQuery)
}

// A failing authentication on an attribution error must be impossible.
func TestStrictAttribution_AuthErrorNeverReached(t *testing.T) {
	_, input, _ := strictSetup(t, "/items", "http://example.test/items?id=1",
		&openapi3filter.Options{
			StrictQueryParameterAttribution: true,
			AuthenticationFunc: func(ctx context.Context, input *openapi3filter.AuthenticationInput) error {
				return errors.New("auth must not run")
			},
		})
	err := openapi3filter.ValidateRequest(context.Background(), input)
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "auth must not run"))
}
