// Package openapi3plan builds a small, deterministic set of typed request
// cases for a single OpenAPI operation such that every feasible pair of
// parameter values (pairwise coverage) occurs in at least one emitted case.
//
// Planning is entirely offline: it never invokes authentication callbacks and
// never sends an HTTP request. Every generated case is checked with the same
// parameter validation rules used by openapi3filter.ValidateRequest, followed
// by an optional caller-supplied cross-parameter constraint.
//
// The produced set is selected with a deterministic coverage greedy followed
// by redundancy removal; it is small and stable for identical inputs (and for
// identical inputs presented in a different order) but is not claimed to be
// globally minimal.
package openapi3plan
