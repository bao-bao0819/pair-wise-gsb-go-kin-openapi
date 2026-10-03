package openapi3plan

import (
	"context"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
)

// evaluate builds the concrete request for one complete assignment, checks it
// with the ordinary parameter validation rules, then applies the constraint.
// On success it returns the built request so the selected case can reuse it.
type validAssignment struct {
	a       assignment
	request *http.Request
	values  map[string]Candidate
}

func evaluate(ctx context.Context, route *routers.Route, method string, domains []*paramDomain, a assignment, opts *Options) (validAssignment, bool, error) {
	req, values, err := buildRequest(method, route.Path, domains, a)
	if err != nil {
		return validAssignment{}, false, err
	}

	input := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: reqPathParams(domains, a),
		Route:      route,
		Options:    opts.FilterOptions,
	}

	// Validate every parameter through the existing rules. Validating
	// parameters individually uses the same per-parameter schema checks as
	// ValidateRequest while skipping security/authentication handling, so no
	// AuthenticationFunc is ever invoked and nothing is sent over the network.
	for _, d := range domains {
		if err := openapi3filter.ValidateParameter(ctx, input, d.param); err != nil {
			return validAssignment{}, false, nil
		}
	}

	if opts.Constraint != nil && !safeConstraint(opts.Constraint, values) {
		return validAssignment{}, false, nil
	}

	return validAssignment{a: cloneAssignment(a), request: req, values: values}, true, nil
}

func cloneAssignment(a assignment) assignment {
	out := make(assignment, len(a))
	copy(out, a)
	return out
}

func safeConstraint(c Constraint, values map[string]Candidate) (result bool) {
	defer func() {
		if recover() != nil {
			result = false
		}
	}()
	return c(values)
}
