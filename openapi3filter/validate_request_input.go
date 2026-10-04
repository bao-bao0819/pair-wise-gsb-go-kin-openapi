package openapi3filter

import (
	"net/http"
	"net/url"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
)

// A ContentParameterDecoder takes a parameter definition from the OpenAPI spec,
// and the value which we received for it. It is expected to return the
// value unmarshaled into an interface which can be traversed for
// validation, it should also return the schema to be used for validating the
// object, since there can be more than one in the content spec.
//
// If a query parameter appears multiple times, values[] will have more
// than one  value, but for all other parameter types it should have just
// one.
type ContentParameterDecoder func(param *openapi3.Parameter, values []string) (any, *openapi3.Schema, error)

type RequestValidationInput struct {
	Request      *http.Request
	PathParams   map[string]string
	QueryParams  url.Values
	Route        *routers.Route
	Options      *Options
	ParamDecoder ContentParameterDecoder

	// strictQuery holds the per-parameter query views produced by strict
	// attribution. It is populated before any parameter is decoded so a
	// failure never leaves a half-decoded operation behind.
	strictQuery map[string]url.Values
	// queryAttribution is the report (ambiguous/unconsumed items) produced by
	// strict attribution. Use QueryAttribution to inspect it.
	queryAttribution *QueryAttributionReport
}

func (input *RequestValidationInput) GetQueryParams() url.Values {
	q := input.QueryParams
	if q == nil {
		q = input.Request.URL.Query()
		input.QueryParams = q
	}
	return q
}

// QueryAttribution returns the strict-attribution report for the last
// ValidateRequest run. It is nil unless
// Options.StrictQueryParameterAttribution is enabled. Ambiguous items fail
// validation; the report also lists unconsumed items regardless of whether
// RejectUnconsumedQueryParams rejects them.
func (input *RequestValidationInput) QueryAttribution() *QueryAttributionReport {
	return input.queryAttribution
}

// strictQueryParams returns the query values attributed to the given
// parameter when strict attribution is active, and whether it was applied.
func (input *RequestValidationInput) strictQueryParams(name string) (url.Values, bool) {
	if input.strictQuery == nil {
		return nil, false
	}
	return input.strictQuery[name], true
}
