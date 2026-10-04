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

// Authenticator authenticates a request against the security requirements of
// an operation.
//
// Unlike the plain AuthenticationFunc callback, an Authenticator gets a fresh
// isolated context for every OR branch via BeginBranch. The context returned
// by a scheme is only threaded into the remaining schemes of the same branch;
// it is never visible to any other branch. CommitBranch is called exactly once,
// and only after every scheme of the branch (AND semantics) has succeeded, so a
// half-succeeded branch cannot pollute subsequent branches or the caller's
// request context.
type Authenticator interface {
	// BeginBranch creates an isolated context for a single OR branch. The
	// returned context is used for every AuthenticateScheme call of that
	// branch and then handed to CommitBranch.
	BeginBranch(ctx context.Context, branchIndex int, branch openapi3.SecurityRequirement) context.Context

	// AuthenticateScheme authenticates one scheme (an AND element) of a
	// branch. On success it returns a context that may carry identity state;
	// on failure it returns a non-nil error. It must not mutate the request's
	// original context and must not leak credential values through the
	// returned error.
	AuthenticateScheme(ctx context.Context, input *AuthenticationInput) (context.Context, error)

	// CommitBranch finalizes a fully successful branch and returns the
	// context that should replace the caller's request context. It is called
	// only once, only for a branch whose every scheme succeeded.
	CommitBranch(ctx context.Context, branchIndex int, branch openapi3.SecurityRequirement) context.Context
}

// NewAuthenticator adapts the legacy AuthenticationFunc callback to the
// Authenticator interface. Each branch starts from the base context and a
// successful branch commits the last scheme context unchanged, so existing
// AuthenticationFunc-based services keep working without modification.
func NewAuthenticator(f AuthenticationFunc) Authenticator {
	if f == nil {
		return nil
	}
	return &funcAuthenticator{f: f}
}

type funcAuthenticator struct {
	f AuthenticationFunc
}

func (a *funcAuthenticator) BeginBranch(ctx context.Context, _ int, _ openapi3.SecurityRequirement) context.Context {
	return ctx
}

func (a *funcAuthenticator) AuthenticateScheme(ctx context.Context, input *AuthenticationInput) (context.Context, error) {
	return ctx, a.f(ctx, input)
}

func (a *funcAuthenticator) CommitBranch(ctx context.Context, _ int, _ openapi3.SecurityRequirement) context.Context {
	return ctx
}

// AuthenticationOptions controls AuthenticateRequest.
type AuthenticationOptions struct {
	// ContinueProbingBranches keeps evaluating remaining OR branches after a
	// branch has succeeded. It defaults to false: probing stops at the first
	// successful branch so the authentication service is not called more
	// than necessary.
	ContinueProbingBranches bool
}

// SchemeResult reports the outcome of authenticating one scheme (an AND
// element of a branch). It never contains credential values.
type SchemeResult struct {
	SchemeName string
	SchemeType string
	// Scopes required by the security requirement for this scheme.
	Scopes []string
	// Err holds the error returned by the Authenticator, or by the validator
	// when the scheme is not declared in the components. It is nil on success.
	Err error
}

// BranchResult reports one OR branch.
type BranchResult struct {
	// Index is the zero-based position of the branch within the operation's
	// security requirements.
	Index int
	// Schemes holds, in evaluation order, the schemes that were attempted.
	Schemes []SchemeResult
	// Anonymous is true for an empty requirement, which allows anonymous
	// access and never calls the Authenticator.
	Anonymous bool
	// Succeeded is true when every scheme of the branch succeeded.
	Succeeded bool
	// Committed is true when this is the branch whose context was committed
	// to the caller's request context. At most one branch is committed.
	Committed bool
}

// AuthenticationReport describes how the request was authenticated. It never
// contains credential values.
type AuthenticationReport struct {
	// Branches holds one entry per OR branch that was probed, in order.
	Branches []BranchResult
	// Anonymous is true when access was granted by an empty (but non-nil)
	// security list, either because an operation declared security: [] (which
	// overrides global security) or because the only requirements were empty.
	// No Authenticator call is made in that case.
	Anonymous bool
	// SuccessfulBranch is the index of the first fully successful branch, or
	// -1 when no branch succeeded.
	SuccessfulBranch int
	// SelectedBranch is the index of the committed branch (the first
	// successful branch), or -1 when no branch was committed.
	SelectedBranch int
}

// AuthenticateRequest evaluates the security requirements that apply to the
// request's operation (OR over requirements, AND over the schemes within a
// requirement) and returns:
//
//   - ctx: the request context carrying the committed branch identity, or the
//     passed-in context unchanged when there is no successful branch;
//   - report: a structured description of every branch and scheme probed;
//   - err: nil on success (including anonymous access), otherwise a
//     *SecurityRequirementsError wrapping one error per failed probed branch.
//
// Each OR branch is evaluated against a fresh context created by the
// Authenticator; a branch context is committed to the request context only
// after every scheme of that branch has succeeded, so a partially successful
// failed branch never leaks identity state into later branches.
//
// An empty security requirement denotes anonymous access. An empty (but
// non-nil) operation-level security list overrides the global requirements
// and allows anonymous access; a nil operation-level security falls back to
// the global requirements.
func AuthenticateRequest(ctx context.Context, input *RequestValidationInput, opts *AuthenticationOptions) (context.Context, *AuthenticationReport, error) {
	options := input.Options
	if options == nil {
		options = &Options{}
	}

	security := resolveSecurityRequirements(input)
	return authenticate(ctx, input, options, security, opts)
}

// resolveSecurityRequirements returns the security requirements that apply to
// the request operation, or nil when the spec imposes no security at all.
func resolveSecurityRequirements(input *RequestValidationInput) *openapi3.SecurityRequirements {
	security := input.Route.Operation.Security
	if security == nil {
		security = &input.Route.Spec.Security
	}
	return security
}

// authenticate is the branch-isolating evaluation core. A nil security means
// the spec does not constrain authentication; an empty (non-nil) security
// means anonymous access is explicitly allowed.
func authenticate(
	ctx context.Context,
	input *RequestValidationInput,
	options *Options,
	security *openapi3.SecurityRequirements,
	opts *AuthenticationOptions,
) (context.Context, *AuthenticationReport, error) {
	report := &AuthenticationReport{SuccessfulBranch: -1, SelectedBranch: -1}

	// No security requirement object applies at all: nothing to authenticate.
	if security == nil {
		return ctx, report, nil
	}
	srs := *security

	// An explicit, empty list of requirements allows anonymous access and
	// overrides any global requirements.
	if len(srs) == 0 {
		report.Anonymous = true
		return ctx, report, nil
	}

	authenticator := options.Authenticator
	if authenticator == nil && options.AuthenticationFunc != nil {
		authenticator = NewAuthenticator(options.AuthenticationFunc)
	}
	if authenticator == nil {
		return ctx, report, ErrAuthenticationServiceMissing
	}

	continueProbing := opts != nil && opts.ContinueProbingBranches

	securitySchemes := componentSecuritySchemes(input)

	// Buffer the request body once so every AuthenticateScheme call gets a
	// fresh copy: an authentication function may consume the body.
	data, bodyErr := bufferRequestBody(input.Request)
	if bodyErr != nil {
		return ctx, report, bodyErr
	}
	// Leave a fresh body for later request validation regardless of how many
	// schemes consumed it or which branch was selected.
	defer resetRequestBody(input.Request, data)

	var branchErrors []error

	for branchIndex, branch := range srs {
		// An empty requirement is anonymous access: it always succeeds and
		// never invokes the Authenticator.
		if len(branch) == 0 {
			report.Anonymous = true
			report.Branches = append(report.Branches, BranchResult{
				Index:     branchIndex,
				Anonymous: true,
				Succeeded: true,
				Committed: true,
			})
			report.SuccessfulBranch = branchIndex
			report.SelectedBranch = branchIndex
			// Anonymous access commits no identity.
			return ctx, report, nil
		}

		result := BranchResult{Index: branchIndex}

		names := make([]string, 0, len(branch))
		for name := range branch {
			names = append(names, name)
		}
		slices.Sort(names)

		// Fresh, isolated context for this branch, derived from the base
		// request context rather than from any earlier branch.
		branchCtx := authenticator.BeginBranch(ctx, branchIndex, branch)
		branchSucceeded := true

		for _, name := range names {
			scheme := lookupSecurityScheme(securitySchemes, name)
			scopes := slices.Clone(branch[name])

			schemeResult := SchemeResult{
				SchemeName: name,
				Scopes:     scopes,
			}
			if scheme != nil {
				schemeResult.SchemeType = scheme.Type
			}

			if scheme == nil {
				schemeResult.Err = fmt.Errorf("security scheme %q is not declared", name)
				result.Schemes = append(result.Schemes, schemeResult)
				branchSucceeded = false
				break
			}

			resetRequestBody(input.Request, data)

			authInput := &AuthenticationInput{
				RequestValidationInput: input,
				SecuritySchemeName:     name,
				SecurityScheme:         scheme,
				Scopes:                 scopes,
			}
			nextCtx, err := authenticator.AuthenticateScheme(branchCtx, authInput)
			if err != nil {
				// The Authenticator contract forbids putting credential
				// values in this error, so it is safe to surface in the
				// report and error chain.
				schemeResult.Err = err
				result.Schemes = append(result.Schemes, schemeResult)
				branchSucceeded = false
				break
			}
			if nextCtx != nil {
				branchCtx = nextCtx
			}
			result.Schemes = append(result.Schemes, schemeResult)
		}

		result.Succeeded = branchSucceeded

		if branchSucceeded {
			if report.SuccessfulBranch == -1 {
				report.SuccessfulBranch = branchIndex
				committedCtx := authenticator.CommitBranch(branchCtx, branchIndex, branch)
				if committedCtx != nil {
					ctx = committedCtx
				}
				report.SelectedBranch = branchIndex
				result.Committed = true
			}
			report.Branches = append(report.Branches, result)
			branchErrors = append(branchErrors, nil)
			if !continueProbing {
				return ctx, report, nil
			}
			continue
		}

		report.Branches = append(report.Branches, result)
		branchErrors = append(branchErrors, &securityRequirementBranchError{
			branchIndex: branchIndex,
			schemes:     result.Schemes,
		})
	}

	if report.SelectedBranch != -1 {
		return ctx, report, nil
	}

	failed := make([]error, 0, len(branchErrors))
	for _, err := range branchErrors {
		if err != nil {
			failed = append(failed, err)
		}
	}
	return ctx, report, &SecurityRequirementsError{
		SecurityRequirements: srs,
		Errors:               failed,
	}
}

// securityRequirementBranchError wraps the per-scheme error chain of one OR
// branch that did not fully succeed.
type securityRequirementBranchError struct {
	branchIndex int
	schemes     []SchemeResult
}

func (e *securityRequirementBranchError) Error() string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "security requirement %d failed:", e.branchIndex)
	printed := 0
	for _, scheme := range e.schemes {
		if scheme.Err == nil {
			continue
		}
		if printed > 0 {
			b.WriteString(";")
		}
		if len(scheme.Scopes) == 0 {
			fmt.Fprintf(&b, " %s: %v", scheme.SchemeName, scheme.Err)
		} else {
			fmt.Fprintf(&b, " %s (scopes: %v): %v", scheme.SchemeName, scheme.Scopes, scheme.Err)
		}
		printed++
	}
	if printed == 0 {
		b.WriteString(" no scheme satisfied")
	}
	return b.String()
}

var _ interface{ Unwrap() []error } = (*securityRequirementBranchError)(nil)

func (e *securityRequirementBranchError) Unwrap() []error {
	errs := make([]error, 0, len(e.schemes))
	for _, scheme := range e.schemes {
		if scheme.Err != nil {
			errs = append(errs, scheme.Err)
		}
	}
	return errs
}

func componentSecuritySchemes(input *RequestValidationInput) openapi3.SecuritySchemes {
	if components := input.Route.Spec.Components; components != nil {
		return components.SecuritySchemes
	}
	return nil
}

func lookupSecurityScheme(schemes openapi3.SecuritySchemes, name string) *openapi3.SecurityScheme {
	if schemes == nil {
		return nil
	}
	ref := schemes[name]
	if ref == nil {
		return nil
	}
	return ref.Value
}

// bufferRequestBody reads and closes the request body once and returns its
// contents. A nil result means there was no body to buffer.
func bufferRequestBody(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	defer req.Body.Close()
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// resetRequestBody gives the request a fresh reader over the buffered body so
// the next AuthenticateScheme call can consume it.
func resetRequestBody(req *http.Request, data []byte) {
	if req == nil || data == nil {
		return
	}
	req.Body = nil
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			req.Body = body
			return
		}
	}
	req.ContentLength = int64(len(data))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	req.Body, _ = req.GetBody()
}
