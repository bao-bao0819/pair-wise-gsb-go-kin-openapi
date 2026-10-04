package openapi3filter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
)

// authEvent records one interaction with the in-memory Authenticator. It never
// stores credential values.
type authEvent struct {
	kind   string // "begin", "scheme", "commit"
	branch int
	scheme string
	ok     bool
}

type identityKey string

// memoryAuthenticator is a pure in-memory Authenticator. Branch state lives
// only in the branch context; nothing is written to the outer request context
// until CommitBranch.
type memoryAuthenticator struct {
	events []authEvent
}

func (a *memoryAuthenticator) BeginBranch(ctx context.Context, branchIndex int, _ openapi3.SecurityRequirement) context.Context {
	a.events = append(a.events, authEvent{kind: "begin", branch: branchIndex})
	return ctx
}

func (a *memoryAuthenticator) AuthenticateScheme(ctx context.Context, input *AuthenticationInput) (context.Context, error) {
	req := input.RequestValidationInput.Request

	switch input.SecurityScheme.Type {
	case "apiKey":
		a.events = append(a.events, authEvent{kind: "scheme", scheme: input.SecuritySchemeName})
		idx := len(a.events) - 1
		if req.Header.Get("X-Api-Key") == "" {
			return ctx, errors.New("api key not present")
		}
		a.events[idx].ok = true
		return context.WithValue(ctx, identityKey("apiKey"), "api-key-principal"), nil

	case "oauth2":
		a.events = append(a.events, authEvent{kind: "scheme", scheme: input.SecuritySchemeName})
		idx := len(a.events) - 1
		token := req.Header.Get("Authorization")
		if !strings.HasPrefix(token, "Bearer ") || token != "Bearer good-token" {
			// Never echo the token value back in the error.
			return ctx, errors.New("oauth2 access token rejected")
		}
		a.events[idx].ok = true
		return context.WithValue(ctx, identityKey("oauth2"), "oauth-principal"), nil

	case "mutualTLS":
		a.events = append(a.events, authEvent{kind: "scheme", scheme: input.SecuritySchemeName})
		idx := len(a.events) - 1
		if req.TLS == nil || len(req.TLS.PeerCertificates) == 0 {
			return ctx, errors.New("mTLS client certificate missing")
		}
		a.events[idx].ok = true
		return context.WithValue(ctx, identityKey("mtls"), "mtls-principal"), nil

	default:
		return ctx, fmt.Errorf("unsupported scheme type %q", input.SecurityScheme.Type)
	}
}

func (a *memoryAuthenticator) CommitBranch(ctx context.Context, branchIndex int, _ openapi3.SecurityRequirement) context.Context {
	a.events = append(a.events, authEvent{kind: "commit", branch: branchIndex, ok: true})
	return context.WithValue(ctx, identityKey("committed"), branchIndex)
}

func (a *memoryAuthenticator) eventKinds() []string {
	kinds := make([]string, 0, len(a.events))
	for _, e := range a.events {
		kinds = append(kinds, e.kind)
	}
	return kinds
}

const branchAuthSpec = `
openapi: 3.1.0
info:
  title: 'Branch auth'
  version: '0.0.1'
security:
  - oauth2: [read]
    tls: []
  - apiKey: []
paths:
  /data:
    get:
      responses:
        '200':
          description: ok
  /submit:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name:
                  type: string
      responses:
        '201':
          description: created
  /public:
    get:
      security: []
      responses:
        '200':
          description: ok
components:
  securitySchemes:
    apiKey:
      type: apiKey
      name: X-Api-Key
      in: header
    oauth2:
      type: oauth2
      flows:
        clientCredentials:
          tokenUrl: https://example.com/token
          scopes:
            read: read access
    tls:
      type: mutualTLS
`

func withClientCert(req *http.Request) {
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
}

func newAuthRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, path, nil)
	require.NoError(t, err)
	return req
}

func newAuthInput(t *testing.T, spec, path string, authenticator Authenticator) (*RequestValidationInput, *http.Request) {
	t.Helper()
	router := setupTestRouter(t, spec)
	req := newAuthRequest(t, path)
	route, pathParams, err := router.FindRoute(req)
	require.NoError(t, err)
	return &RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &Options{Authenticator: authenticator},
	}, req
}

func TestAuthenticateRequest_FirstBranchHalfSucceedsSecondBranchSucceeds(t *testing.T) {
	auth := &memoryAuthenticator{}
	input, req := newAuthInput(t, branchAuthSpec, "/data", auth)

	// Valid OAuth2 token but no client certificate => branch 0 half-succeeds
	// (oauth2 OK, mtls fails). Valid API key => branch 1 fully succeeds.
	req.Header.Set("Authorization", "Bearer good-token")
	req.Header.Set("X-Api-Key", "secret-key")

	base := context.Background()
	ctx, report, err := AuthenticateRequest(base, input, nil)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Only the first successful branch is probed; no extra auth service calls.
	assert.Equal(t, []string{"begin", "scheme", "scheme", "begin", "scheme", "commit"}, auth.eventKinds())

	// Branch 0: oauth2 succeeded, mtls failed, nothing committed.
	require.Len(t, report.Branches, 2)
	b0 := report.Branches[0]
	assert.Equal(t, 0, b0.Index)
	assert.False(t, b0.Succeeded)
	assert.False(t, b0.Committed)
	require.Len(t, b0.Schemes, 2)
	assert.Equal(t, "oauth2", b0.Schemes[0].SchemeName)
	assert.Equal(t, []string{"read"}, b0.Schemes[0].Scopes)
	assert.NoError(t, b0.Schemes[0].Err)
	assert.Equal(t, "tls", b0.Schemes[1].SchemeName)
	require.Error(t, b0.Schemes[1].Err)
	assert.Equal(t, "mTLS client certificate missing", b0.Schemes[1].Err.Error())

	// Branch 1: apiKey succeeded and was committed.
	b1 := report.Branches[1]
	assert.True(t, b1.Succeeded)
	assert.True(t, b1.Committed)
	require.Len(t, b1.Schemes, 1)
	assert.Equal(t, "apiKey", b1.Schemes[0].SchemeName)
	assert.NoError(t, b0.Schemes[0].Err)

	assert.Equal(t, 1, report.SuccessfulBranch)
	assert.Equal(t, 1, report.SelectedBranch)

	// Only branch 1 identity is committed. The failed branch's oauth2
	// identity must not leak, and no credential values are present.
	assert.Equal(t, 1, ctx.Value(identityKey("committed")))
	assert.Equal(t, "api-key-principal", ctx.Value(identityKey("apiKey")))
	assert.Nil(t, ctx.Value(identityKey("oauth2")))
	assert.Nil(t, ctx.Value(identityKey("mtls")))
	assert.NotContains(t, fmt.Sprintf("%v", report), "good-token")
	assert.NotContains(t, fmt.Sprintf("%v", report), "secret-key")
}

func TestAuthenticateRequest_AllBranchesFail(t *testing.T) {
	auth := &memoryAuthenticator{}
	input, req := newAuthInput(t, branchAuthSpec, "/data", auth)
	// Valid OAuth2 token but no mTLS (branch 0 half-succeeds), no API key
	// (branch 1 fails).
	req.Header.Set("Authorization", "Bearer good-token")

	base := context.WithValue(context.Background(), identityKey("base"), "preserved")
	ctx, report, err := AuthenticateRequest(base, input, nil)

	var secErr *SecurityRequirementsError
	require.ErrorAs(t, err, &secErr)
	require.Len(t, secErr.Errors, 2)

	// Both branches were attempted; neither committed.
	assert.Equal(t, []string{"begin", "scheme", "scheme", "begin", "scheme"}, auth.eventKinds())

	require.NotNil(t, report)
	assert.Equal(t, -1, report.SuccessfulBranch)
	assert.Equal(t, -1, report.SelectedBranch)
	require.Len(t, report.Branches, 2)
	assert.False(t, report.Branches[0].Succeeded)
	assert.False(t, report.Branches[1].Succeeded)
	for _, b := range report.Branches {
		assert.False(t, b.Committed)
	}

	// No identity is committed to the outer request context.
	assert.Nil(t, ctx.Value(identityKey("committed")))
	assert.Nil(t, ctx.Value(identityKey("oauth2")))
	assert.Nil(t, ctx.Value(identityKey("mtls")))
	assert.Nil(t, ctx.Value(identityKey("apiKey")))
	assert.Equal(t, "preserved", ctx.Value(identityKey("base")))

	// Error chain is preserved and free of credential values.
	msg := err.Error()
	assert.Contains(t, msg, "security requirements failed")
	assert.Contains(t, msg, "mTLS client certificate missing")
	assert.Contains(t, msg, "api key not present")
	assert.NotContains(t, msg, "good-token")

	var joined interface{ Unwrap() []error }
	require.True(t, errors.As(err, &joined))
	require.Len(t, joined.Unwrap(), 2)
}

func TestAuthenticateRequest_ContinueProbingBranches(t *testing.T) {
	auth := &memoryAuthenticator{}
	input, req := newAuthInput(t, branchAuthSpec, "/data", auth)
	// Both branches can succeed.
	withClientCert(req)
	req.Header.Set("Authorization", "Bearer good-token")
	req.Header.Set("X-Api-Key", "secret-key")

	// Without the option probing stops after the first successful branch.
	auth.events = nil
	_, report, err := AuthenticateRequest(context.Background(), input, nil)
	require.NoError(t, err)
	assert.Len(t, report.Branches, 1)
	assert.Equal(t, []string{"begin", "scheme", "scheme", "commit"}, auth.eventKinds())

	// With ContinueProbingBranches both branches are evaluated, but only the
	// first successful branch is committed.
	auth.events = nil
	ctx, report, err := AuthenticateRequest(context.Background(), input, &AuthenticationOptions{
		ContinueProbingBranches: true,
	})
	require.NoError(t, err)
	require.Len(t, report.Branches, 2)
	assert.Equal(t, []string{"begin", "scheme", "scheme", "commit", "begin", "scheme"}, auth.eventKinds())
	assert.True(t, report.Branches[0].Committed)
	assert.False(t, report.Branches[1].Committed)
	assert.Equal(t, 0, report.SelectedBranch)
	assert.Equal(t, 0, ctx.Value(identityKey("committed")))
	// Branch 1 identity is not committed.
	assert.Nil(t, ctx.Value(identityKey("apiKey")))
}

func TestAuthenticateRequest_AnonymousOperationOverridesGlobal(t *testing.T) {
	auth := &memoryAuthenticator{}
	input, _ := newAuthInput(t, branchAuthSpec, "/public", auth)

	ctx, report, err := AuthenticateRequest(context.Background(), input, nil)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.True(t, report.Anonymous)
	assert.Empty(t, report.Branches)
	assert.Equal(t, -1, report.SelectedBranch)
	assert.Empty(t, auth.events)
	assert.Nil(t, ctx.Value(identityKey("committed")))
}

func TestValidateRequest_BranchAuthenticatorPopulatesReport(t *testing.T) {
	auth := &memoryAuthenticator{}
	input, req := newAuthInput(t, branchAuthSpec, "/data", auth)
	req.Header.Set("X-Api-Key", "secret-key")

	err := ValidateRequest(context.Background(), input)
	require.NoError(t, err)

	require.NotNil(t, input.AuthenticationReport)
	assert.Equal(t, 1, input.AuthenticationReport.SelectedBranch)
	assert.Len(t, input.AuthenticationReport.Branches, 2)
}

func TestValidateRequest_BranchAuthenticatorFailure(t *testing.T) {
	auth := &memoryAuthenticator{}
	input, _ := newAuthInput(t, branchAuthSpec, "/data", auth)
	// No credentials at all.

	err := ValidateRequest(context.Background(), input)
	var secErr *SecurityRequirementsError
	require.ErrorAs(t, err, &secErr)
	require.NotNil(t, input.AuthenticationReport)
	assert.Equal(t, -1, input.AuthenticationReport.SelectedBranch)
}

func TestAuthenticateRequest_LegacyAuthenticationFuncAdapter(t *testing.T) {
	called := 0
	legacy := func(_ context.Context, input *AuthenticationInput) error {
		called++
		switch input.SecurityScheme.Type {
		case "apiKey":
			if input.RequestValidationInput.Request.Header.Get("X-Api-Key") == "" {
				return errors.New("api key not present")
			}
		case "mutualTLS":
			req := input.RequestValidationInput.Request
			if req.TLS == nil || len(req.TLS.PeerCertificates) == 0 {
				return errors.New("mTLS client certificate missing")
			}
		}
		return nil
	}

	input, req := newAuthInput(t, branchAuthSpec, "/data", NewAuthenticator(legacy))
	req.Header.Set("Authorization", "Bearer good-token")
	req.Header.Set("X-Api-Key", "secret-key")

	ctx, report, err := AuthenticateRequest(context.Background(), input, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, called) // oauth2 + mtls in branch 0, apiKey in branch 1
	assert.Equal(t, 1, report.SelectedBranch)
	assert.Nil(t, ctx.Value(identityKey("committed"))) // adapter commits context unchanged
}

func TestAuthenticateRequest_MissingAuthenticator(t *testing.T) {
	router := setupTestRouter(t, branchAuthSpec)
	req := newAuthRequest(t, "/data")
	route, pathParams, err := router.FindRoute(req)
	require.NoError(t, err)
	input := &RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &Options{},
	}

	ctx, report, err := AuthenticateRequest(context.Background(), input, nil)
	require.ErrorIs(t, err, ErrAuthenticationServiceMissing)
	assert.Equal(t, -1, report.SelectedBranch)
	assert.Empty(t, report.Branches)
	assert.Nil(t, ctx.Value(identityKey("committed")))
}

// bodyConsumingAuthenticator reads the entire request body while checking the
// API key, proving that later request-body validation still sees a fresh body
// after branch-isolated authentication.
type bodyConsumingAuthenticator struct{}

func (bodyConsumingAuthenticator) BeginBranch(ctx context.Context, _ int, _ openapi3.SecurityRequirement) context.Context {
	return ctx
}

func (bodyConsumingAuthenticator) AuthenticateScheme(ctx context.Context, input *AuthenticationInput) (context.Context, error) {
	if input.SecurityScheme.Type == "apiKey" &&
		input.RequestValidationInput.Request.Header.Get("X-Api-Key") == "" {
		return ctx, errors.New("api key not present")
	}
	if input.RequestValidationInput.Request.Body != nil {
		_, _ = io.ReadAll(input.RequestValidationInput.Request.Body)
	}
	return ctx, nil
}

func (bodyConsumingAuthenticator) CommitBranch(ctx context.Context, _ int, _ openapi3.SecurityRequirement) context.Context {
	return ctx
}

func TestValidateRequest_BodyStillValidatedAfterAuthentication(t *testing.T) {
	const spec = `
openapi: 3.1.0
info:
  title: 'Body after auth'
  version: '0.0.1'
security:
  - apiKey: []
paths:
  /submit:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name:
                  type: string
      responses:
        '201':
          description: created
components:
  securitySchemes:
    apiKey:
      type: apiKey
      name: X-Api-Key
      in: header
`
	router := setupTestRouter(t, spec)
	req, err := http.NewRequest(http.MethodPost, "/submit", strings.NewReader(`{"name":"hello"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "secret-key")

	route, pathParams, err := router.FindRoute(req)
	require.NoError(t, err)
	input := &RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &Options{Authenticator: bodyConsumingAuthenticator{}},
	}

	require.NoError(t, ValidateRequest(context.Background(), input))
	require.NotNil(t, input.AuthenticationReport)
	assert.Equal(t, 0, input.AuthenticationReport.SelectedBranch)
}
