package openapi3filter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
)

// AuthenticatedIdentity describes a principal established while evaluating a
// security scheme. It intentionally carries no credential material: schemes
// are identified by name and required scopes only.
type AuthenticatedIdentity struct {
	// SecuritySchemeName is the name of the security scheme that
	// authenticated the request.
	SecuritySchemeName string

	// Scopes are the scopes required by the security requirement.
	Scopes []string

	// Subject identifies the authenticated principal, e.g. a user ID.
	Subject string
}

// authStateContextKey is the context key under which the authentication
// state is carried.
type authStateContextKey struct{}

// authenticationState accumulates committed identities. A scratch state is
// created per OR branch and only merged into the parent state once the whole
// branch succeeds.
type authenticationState struct {
	identities []AuthenticatedIdentity
}

func (state *authenticationState) snapshot() []AuthenticatedIdentity {
	return slices.Clone(state.identities)
}

// NewAuthenticationContext derives a context carrying a fresh authentication
// state. The returned function reports the identities committed to that
// state by successful security requirement branches.
func NewAuthenticationContext(ctx context.Context) (context.Context, func() []AuthenticatedIdentity) {
	state := &authenticationState{}
	return context.WithValue(ctx, authStateContextKey{}, state), state.snapshot
}

// CommitAuthentication records an authenticated identity into the
// authentication state carried by ctx. It is meant to be called from an
// AuthenticationFunc. While a security requirement branch is being
// evaluated, the identity is staged in a scratch state isolated from the
// caller's context; it only becomes visible through the function returned by
// NewAuthenticationContext once the whole branch succeeds. It reports
// whether ctx carried an authentication state.
func CommitAuthentication(ctx context.Context, identity AuthenticatedIdentity) bool {
	state, ok := ctx.Value(authStateContextKey{}).(*authenticationState)
	if !ok {
		return false
	}
	state.identities = append(state.identities, identity)
	return true
}

// SecurityRequirementsOptions tunes ValidateSecurityRequirementsWithReport.
type SecurityRequirementsOptions struct {
	// ContinueOnSuccess keeps evaluating the remaining OR branches after a
	// branch has succeeded, so the report describes every usable security
	// alternative. By default evaluation stops at the first successful
	// branch and no further calls to the authentication service are made.
	ContinueOnSuccess bool
}

// SecuritySchemeReport describes the outcome of a single security scheme
// within a requirement branch. It never contains credential values.
type SecuritySchemeReport struct {
	// Name is the security scheme name.
	Name string

	// Scopes are the scopes required by the security requirement.
	Scopes []string

	// Err is the error chain returned by the AuthenticationFunc, if any.
	Err error
}

// SecurityBranchReport describes the outcome of a single OR branch, i.e. one
// openapi3.SecurityRequirement whose schemes are ANDed together.
type SecurityBranchReport struct {
	// Index is the position of the requirement in the SecurityRequirements.
	Index int

	// Anonymous reports whether the requirement is empty, meaning
	// unauthenticated access is allowed.
	Anonymous bool

	// Succeeded reports whether every scheme of the branch was satisfied.
	Succeeded bool

	// Schemes holds one report per evaluated scheme, in evaluation order.
	Schemes []SecuritySchemeReport

	// Identities are the identities committed by the branch. They are only
	// merged into the caller's authentication context when Succeeded is
	// true.
	Identities []AuthenticatedIdentity

	// Err is the error that made the branch fail: the first failing
	// scheme's error chain, or a declaration error.
	Err error
}

// SecurityRequirementsReport describes how a set of security requirements
// was evaluated. It never contains credential values.
type SecurityRequirementsReport struct {
	// Branches holds one report per evaluated OR branch, in order. Unless
	// SecurityRequirementsOptions.ContinueOnSuccess is set, evaluation stops
	// at the first successful branch and later branches are not reported.
	Branches []SecurityBranchReport

	// Matched is the index of the first successful branch, or -1 when no
	// branch succeeded.
	Matched int
}

// ValidateSecurityRequirementsWithReport evaluates security requirements
// like ValidateSecurityRequirements and additionally returns a report
// describing which branch and schemes were used. Schemes within one
// requirement are ANDed, requirements are ORed, and an empty requirement
// allows anonymous access. Each branch is evaluated against an isolated
// scratch authentication context: identities staged with
// CommitAuthentication are only committed to the caller's context (see
// NewAuthenticationContext) once the whole branch succeeds, so a failed
// branch cannot pollute the evaluation of the next one. When no branch
// succeeds, no identity is committed at all.
func ValidateSecurityRequirementsWithReport(ctx context.Context, input *RequestValidationInput, srs openapi3.SecurityRequirements, opts *SecurityRequirementsOptions) (*SecurityRequirementsReport, error) {
	report := &SecurityRequirementsReport{Matched: -1}
	if len(srs) == 0 {
		return report, nil
	}
	continueOnSuccess := opts != nil && opts.ContinueOnSuccess

	var errs []error
	for i, sr := range srs {
		branch, state := validateSecurityBranch(ctx, input, i, sr)
		report.Branches = append(report.Branches, branch)
		if !branch.Succeeded {
			errs = append(errs, branch.Err)
			continue
		}
		if report.Matched == -1 {
			report.Matched = i
		}
		if parent, ok := ctx.Value(authStateContextKey{}).(*authenticationState); ok && parent != state {
			parent.identities = append(parent.identities, state.identities...)
		}
		if !continueOnSuccess {
			break
		}
	}
	if report.Matched == -1 {
		return report, &SecurityRequirementsError{
			SecurityRequirements: srs,
			Errors:               errs,
		}
	}
	return report, nil
}

// validateSecurityBranch validates a single OpenAPI 3 security requirement
// against an isolated scratch authentication state, which is returned along
// with the branch report.
func validateSecurityBranch(ctx context.Context, input *RequestValidationInput, index int, securityRequirement openapi3.SecurityRequirement) (SecurityBranchReport, *authenticationState) {
	state := &authenticationState{}
	branch := SecurityBranchReport{
		Index:     index,
		Anonymous: len(securityRequirement) == 0,
	}

	names := make([]string, 0, len(securityRequirement))
	for name := range securityRequirement {
		names = append(names, name)
	}
	slices.Sort(names)

	// Get authentication function
	options := input.Options
	if options == nil {
		options = &Options{}
	}
	f := options.AuthenticationFunc
	if f == nil {
		branch.Err = ErrAuthenticationServiceMissing
		return branch, state
	}

	var securitySchemes openapi3.SecuritySchemes
	if components := input.Route.Spec.Components; components != nil {
		securitySchemes = components.SecuritySchemes
	}

	// NOTE that because we could have an `AuthenticationFunc` that reads the request body, we need to provide a fresh `io.Reader` to each iteration of the loop. To make this more performant, we can read the request body once into memory (which may be costly) and then create a fresh `io.Reader` for each `AuthenticationFunc`
	var data []byte

	if input.Request != nil && input.Request.Body != http.NoBody && input.Request.Body != nil {
		defer input.Request.Body.Close()

		var err error
		if data, err = io.ReadAll(input.Request.Body); err != nil {
			branch.Err = &RequestError{
				Input:  input,
				Reason: "reading failed",
				Err:    err,
			}
			return branch, state
		}
	}

	// Evaluate the branch against a scratch authentication state so that a
	// partially successful branch cannot pollute the caller's context.
	branchCtx := context.WithValue(ctx, authStateContextKey{}, state)

	// For each scheme for the requirement
	for _, name := range names {
		var securityScheme *openapi3.SecurityScheme
		if securitySchemes != nil {
			if ref := securitySchemes[name]; ref != nil {
				securityScheme = ref.Value
			}
		}
		scopes := securityRequirement[name]
		schemeReport := SecuritySchemeReport{Name: name, Scopes: slices.Clone(scopes)}
		if securityScheme == nil {
			schemeReport.Err = &RequestError{
				Input: input,
				Err:   fmt.Errorf("security scheme %q is not declared", name),
			}
			branch.Schemes = append(branch.Schemes, schemeReport)
			branch.Err = schemeReport.Err
			return branch, state
		}

		// if there was a request body, then make sure we provide a new copy of the body in the `input`
		if data != nil {
			resetRequestBody(input, data)
		}

		if err := f(branchCtx, &AuthenticationInput{
			RequestValidationInput: input,
			SecuritySchemeName:     name,
			SecurityScheme:         securityScheme,
			Scopes:                 scopes,
		}); err != nil {
			schemeReport.Err = err
			branch.Schemes = append(branch.Schemes, schemeReport)
			branch.Err = err
			return branch, state
		}
		branch.Schemes = append(branch.Schemes, schemeReport)
	}

	// if there was a request body, then make sure we put it back into the `input`
	if data != nil {
		resetRequestBody(input, data)
	}
	branch.Succeeded = true
	branch.Identities = state.snapshot()
	return branch, state
}

// resetRequestBody replaces the request body of input with a fresh reader
// over data.
func resetRequestBody(input *RequestValidationInput, data []byte) {
	input.Request.Body = nil
	if input.Request.GetBody != nil {
		var err error
		if input.Request.Body, err = input.Request.GetBody(); err != nil {
			input.Request.Body = nil
		}
	}
	if input.Request.Body == nil {
		input.Request.ContentLength = int64(len(data))
		input.Request.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		}
		input.Request.Body, _ = input.Request.GetBody() // no error return
	}
}
