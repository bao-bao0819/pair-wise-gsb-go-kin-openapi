package openapi3filter_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3filter"
)

// Path-item-level parameters are part of the plan: pathOnly is a plain named
// query parameter and must be consumed without being reported as unconsumed,
// even though it is not listed on the operation.
func TestStrictAttribution_PathItemParameterConsumed(t *testing.T) {
	_, input, _ := strictSetup(t, "/pathparam", "http://example.test/pathparam?pathOnly=v&id=1",
		&openapi3filter.Options{StrictQueryParameterAttribution: true})
	require.NoError(t, openapi3filter.ValidateRequest(context.Background(), input))
	require.Empty(t, input.QueryAttribution().Unconsumed)
}

// A static conflict aborts before parameter/body validation even when
// MultiError is requested, so callers never receive a half-decoded operation.
func TestStrictAttribution_CompileErrorShortCircuitsMultiError(t *testing.T) {
	_, input, authCalled := strictSetup(t, "/explicit", "http://example.test/explicit?id=1",
		&openapi3filter.Options{
			StrictQueryParameterAttribution: true,
			MultiError:                      true,
		})
	err := openapi3filter.ValidateRequest(context.Background(), input)
	var planErr *openapi3filter.ParameterPlanError
	require.ErrorAs(t, err, &planErr)
	require.False(t, authCalled.Load())
}
