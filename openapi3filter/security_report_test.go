package openapi3filter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
)

const testAPIKeyCredential = "secret-key-value"

// securityRecorder is an in-memory AuthenticationFunc that records the order
// of authentication calls and identity commits.
type securityRecorder struct {
	events []string
	errs   map[string]error
}

func (r *securityRecorder) authenticate(ctx context.Context, input *AuthenticationInput) error {
	r.events = append(r.events, "call:"+input.SecuritySchemeName)
	CommitAuthentication(ctx, AuthenticatedIdentity{
		SecuritySchemeName: input.SecuritySchemeName,
		Scopes:             input.Scopes,
		Subject:            "subject-for-" + input.SecuritySchemeName,
	})
	r.events = append(r.events, "commit:"+input.SecuritySchemeName)
	return r.errs[input.SecuritySchemeName]
}

func newSecurityReportInput(authFunc AuthenticationFunc) *RequestValidationInput {
	doc := &openapi3.T{
		OpenAPI: "3.0.0",
		Info:    &openapi3.Info{Title: "security report", Version: "1.0"},
		Components: &openapi3.Components{
			SecuritySchemes: openapi3.SecuritySchemes{
				"apiKey": {Value: &openapi3.SecurityScheme{Type: "apiKey", In: "header", Name: "X-API-Key"}},
				"oauth": {Value: &openapi3.SecurityScheme{Type: "oauth2", Flows: &openapi3.OAuthFlows{
					ClientCredentials: &openapi3.OAuthFlow{TokenURL: "https://example.com/token"},
				}}},
			},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/items", nil)
	req.Header.Set("X-API-Key", testAPIKeyCredential)
	return &RequestValidationInput{
		Request: req,
		Route:   &routers.Route{Spec: doc},
		Options: &Options{AuthenticationFunc: authFunc},
	}
}

// TestSecurityReportHalfSuccessThenSuccess verifies that a first branch whose
// ANDed schemes only partially succeed does not pollute the second branch,
// and that only the fully successful branch is committed.
func TestSecurityReportHalfSuccessThenSuccess(t *testing.T) {
	recorder := &securityRecorder{errs: map[string]error{
		"oauth": errors.New("oauth: insufficient scope"),
	}}
	input := newSecurityReportInput(recorder.authenticate)
	srs := openapi3.SecurityRequirements{
		{"apiKey": {}, "oauth": {"read"}},
		{"apiKey": {}},
	}

	ctx, committed := NewAuthenticationContext(context.Background())
	report, err := ValidateSecurityRequirementsWithReport(ctx, input, srs, nil)
	require.NoError(t, err)

	// Schemes of a requirement are ANDed and evaluated in sorted order.
	require.Equal(t, []string{
		"call:apiKey", "commit:apiKey",
		"call:oauth", "commit:oauth",
		"call:apiKey", "commit:apiKey",
	}, recorder.events)

	require.Equal(t, 1, report.Matched)
	require.Len(t, report.Branches, 2)

	branch0 := report.Branches[0]
	require.False(t, branch0.Succeeded)
	require.EqualError(t, branch0.Err, "oauth: insufficient scope")
	require.Len(t, branch0.Schemes, 2)
	require.NoError(t, branch0.Schemes[0].Err)
	require.Equal(t, "apiKey", branch0.Schemes[0].Name)
	require.EqualError(t, branch0.Schemes[1].Err, "oauth: insufficient scope")
	require.Equal(t, []string{"read"}, branch0.Schemes[1].Scopes)

	branch1 := report.Branches[1]
	require.True(t, branch1.Succeeded)
	require.NoError(t, branch1.Err)
	require.Len(t, branch1.Identities, 1)
	require.Equal(t, "subject-for-apiKey", branch1.Identities[0].Subject)

	// Only the successful branch is committed to the caller's context: the
	// identity staged by the failed first branch is discarded.
	identities := committed()
	require.Len(t, identities, 1)
	require.Equal(t, "apiKey", identities[0].SecuritySchemeName)
}

// TestSecurityReportAllBranchesFail verifies that when no branch succeeds,
// no identity is committed to the caller's request context and the report
// carries the per-branch error chains without credential material.
func TestSecurityReportAllBranchesFail(t *testing.T) {
	recorder := &securityRecorder{errs: map[string]error{
		"apiKey": fmt.Errorf("apiKey: %w", errors.New("bad key")),
		"oauth":  errors.New("oauth: token expired"),
	}}
	input := newSecurityReportInput(recorder.authenticate)
	srs := openapi3.SecurityRequirements{
		{"apiKey": {}},
		{"oauth": {"write"}},
	}

	ctx, committed := NewAuthenticationContext(context.Background())
	report, err := ValidateSecurityRequirementsWithReport(ctx, input, srs, nil)
	require.Error(t, err)

	var secErr *SecurityRequirementsError
	require.ErrorAs(t, err, &secErr)
	require.Len(t, secErr.Errors, 2)

	require.Equal(t, -1, report.Matched)
	require.Len(t, report.Branches, 2)
	require.EqualError(t, report.Branches[0].Err, "apiKey: bad key")
	require.EqualError(t, report.Branches[1].Err, "oauth: token expired")
	require.Equal(t, []string{"write"}, report.Branches[1].Schemes[0].Scopes)

	// The error chain is preserved for inspection.
	require.ErrorContains(t, report.Branches[0].Err, "bad key")

	// Even though the authentication functions staged identities, nothing
	// was committed to the caller's context.
	require.Empty(t, committed())

	// The report must not leak credential values.
	require.NotContains(t, fmt.Sprintf("%v", report), testAPIKeyCredential)
	require.NotContains(t, err.Error(), testAPIKeyCredential)
}

// TestSecurityReportContinueOnSuccess verifies that by default no extra
// authentication calls are made after the first success, and that probing
// the remaining branches requires the explicit option.
func TestSecurityReportContinueOnSuccess(t *testing.T) {
	srs := openapi3.SecurityRequirements{
		{"apiKey": {}},
		{"oauth": {}},
	}

	recorder := &securityRecorder{}
	input := newSecurityReportInput(recorder.authenticate)
	ctx, committed := NewAuthenticationContext(context.Background())
	report, err := ValidateSecurityRequirementsWithReport(ctx, input, srs, nil)
	require.NoError(t, err)
	require.Equal(t, 0, report.Matched)
	require.Len(t, report.Branches, 1)
	require.Equal(t, []string{"call:apiKey", "commit:apiKey"}, recorder.events)
	require.Len(t, committed(), 1)

	recorder = &securityRecorder{}
	input = newSecurityReportInput(recorder.authenticate)
	ctx, committed = NewAuthenticationContext(context.Background())
	report, err = ValidateSecurityRequirementsWithReport(ctx, input, srs, &SecurityRequirementsOptions{ContinueOnSuccess: true})
	require.NoError(t, err)
	require.Equal(t, 0, report.Matched)
	require.Len(t, report.Branches, 2)
	require.True(t, report.Branches[0].Succeeded)
	require.True(t, report.Branches[1].Succeeded)
	require.Equal(t, []string{
		"call:apiKey", "commit:apiKey",
		"call:oauth", "commit:oauth",
	}, recorder.events)
	require.Len(t, committed(), 2)
}

// TestSecurityReportAnonymousRequirement verifies that an empty requirement
// allows anonymous access without calling the authentication service.
func TestSecurityReportAnonymousRequirement(t *testing.T) {
	recorder := &securityRecorder{}
	input := newSecurityReportInput(recorder.authenticate)
	srs := openapi3.SecurityRequirements{
		{"undeclared": {}},
		{},
	}

	ctx, _ := NewAuthenticationContext(context.Background())
	report, err := ValidateSecurityRequirementsWithReport(ctx, input, srs, nil)
	require.NoError(t, err)
	require.Equal(t, 1, report.Matched)
	require.Len(t, report.Branches, 2)
	require.False(t, report.Branches[0].Succeeded)
	require.ErrorContains(t, report.Branches[0].Err, `security scheme "undeclared" is not declared`)
	require.True(t, report.Branches[1].Anonymous)
	require.True(t, report.Branches[1].Succeeded)
	require.Empty(t, recorder.events)
}

// TestSecurityReportOperationEmptySecurityOverridesGlobal verifies that an
// operation-level empty security requirement overrides the global one.
func TestSecurityReportOperationEmptySecurityOverridesGlobal(t *testing.T) {
	newRoute := func(operationSecurity *openapi3.SecurityRequirements) *routers.Route {
		doc := &openapi3.T{
			OpenAPI: "3.0.0",
			Info:    &openapi3.Info{Title: "security report", Version: "1.0"},
			Security: openapi3.SecurityRequirements{
				{"apiKey": {}},
			},
			Components: &openapi3.Components{
				SecuritySchemes: openapi3.SecuritySchemes{
					"apiKey": {Value: &openapi3.SecurityScheme{Type: "apiKey", In: "header", Name: "X-API-Key"}},
				},
			},
		}
		return &routers.Route{
			Spec:     doc,
			PathItem: &openapi3.PathItem{},
			Operation: &openapi3.Operation{
				Security:  operationSecurity,
				Responses: openapi3.NewResponses(openapi3.WithStatus(200, &openapi3.ResponseRef{Value: openapi3.NewResponse().WithDescription("OK")})),
			},
		}
	}

	// Operation-level empty security: anonymous access, no auth calls.
	recorder := &securityRecorder{}
	input := &RequestValidationInput{
		Request: httptest.NewRequest(http.MethodGet, "http://example.com/items", nil),
		Route:   newRoute(&openapi3.SecurityRequirements{}),
		Options: &Options{AuthenticationFunc: recorder.authenticate},
	}
	require.NoError(t, ValidateRequest(context.Background(), input))
	require.Empty(t, recorder.events)

	// No operation-level security: the global requirement applies.
	recorder = &securityRecorder{}
	input = &RequestValidationInput{
		Request: httptest.NewRequest(http.MethodGet, "http://example.com/items", nil),
		Route:   newRoute(nil),
		Options: &Options{AuthenticationFunc: recorder.authenticate},
	}
	require.NoError(t, ValidateRequest(context.Background(), input))
	require.Equal(t, []string{"call:apiKey", "commit:apiKey"}, recorder.events)
}

// TestSecurityReportCompatEntry verifies that ValidateSecurityRequirements
// keeps its historical behavior on top of the reporting implementation.
func TestSecurityReportCompatEntry(t *testing.T) {
	recorder := &securityRecorder{errs: map[string]error{
		"apiKey": errors.New("apiKey: bad key"),
	}}
	input := newSecurityReportInput(recorder.authenticate)
	srs := openapi3.SecurityRequirements{
		{"apiKey": {}},
		{"oauth": {}},
	}

	require.NoError(t, ValidateSecurityRequirements(context.Background(), input, srs))
	require.Equal(t, []string{
		"call:apiKey", "commit:apiKey",
		"call:oauth", "commit:oauth",
	}, recorder.events)

	recorder.errs["oauth"] = errors.New("oauth: bad token")
	err := ValidateSecurityRequirements(context.Background(), input, srs)
	var secErr *SecurityRequirementsError
	require.ErrorAs(t, err, &secErr)
	require.Len(t, secErr.Errors, 2)

	// No requirements at all: nothing is required, nothing is called.
	recorder.events = nil
	require.NoError(t, ValidateSecurityRequirements(context.Background(), input, nil))
	require.Empty(t, recorder.events)
}

// TestSecurityReportScopesPassedThrough verifies the required scopes reach
// both the AuthenticationFunc and the report unchanged.
func TestSecurityReportScopesPassedThrough(t *testing.T) {
	var gotScopes []string
	input := newSecurityReportInput(func(_ context.Context, ai *AuthenticationInput) error {
		gotScopes = slices.Clone(ai.Scopes)
		return nil
	})
	srs := openapi3.SecurityRequirements{
		{"oauth": {"read", "write"}},
	}

	report, err := ValidateSecurityRequirementsWithReport(context.Background(), input, srs, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"read", "write"}, gotScopes)
	require.Equal(t, []string{"read", "write"}, report.Branches[0].Schemes[0].Scopes)
}
