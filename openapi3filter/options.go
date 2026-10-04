package openapi3filter

import "github.com/getkin/kin-openapi/openapi3"

// Options used by ValidateRequest and ValidateResponse
type Options struct {
	// Set ExcludeRequestBody so ValidateRequest skips request body validation
	ExcludeRequestBody bool

	// Set ExcludeRequestQueryParams so ValidateRequest skips request query params validation
	ExcludeRequestQueryParams bool

	// Set StrictQueryParameterAttribution so ValidateRequest compiles an
	// occupation plan for the whole operation before decoding query
	// parameters. Every raw query item is then attributed to at most one
	// parameter. Statically known parameter collisions fail while compiling
	// the plan; collisions that depend on the request (e.g. two open object
	// parameter sets) fail with a *QueryAttributionError that lists the raw
	// item and every candidate parameter.
	//
	// When unset, parameters are decoded one by one against the whole query
	// string and one raw item may feed several parameters (the historical,
	// permissive behavior).
	StrictQueryParameterAttribution bool

	// Set RejectUnconsumedQueryParams together with
	// StrictQueryParameterAttribution so raw query items that no operation
	// parameter can claim make ValidateRequest fail. Without it such items
	// are reported in *RequestValidationInput.QueryAttribution but ignored.
	RejectUnconsumedQueryParams bool

	// Set ExcludeResponseBody so ValidateResponse skips response body validation
	ExcludeResponseBody bool

	// Set ExcludeReadOnlyValidations so ValidateRequest skips read-only validations
	ExcludeReadOnlyValidations bool

	// Set ExcludeWriteOnlyValidations so ValidateResponse skips write-only validations
	ExcludeWriteOnlyValidations bool

	// Set IncludeResponseStatus so ValidateResponse fails on response
	// status not defined in OpenAPI spec
	IncludeResponseStatus bool

	MultiError bool

	// Set RegexCompiler to override the regex implementation
	RegexCompiler openapi3.RegexCompilerFunc

	// Set RejectWhenRequestBodyNotSpecified so ValidateRequest fails when request body is present but not defined in the specification
	RejectWhenRequestBodyNotSpecified bool

	// A document with security schemes defined will not pass validation
	// unless an AuthenticationFunc is defined.
	// See NoopAuthenticationFunc
	AuthenticationFunc AuthenticationFunc

	// Indicates whether default values are set in the
	// request. If true, then they are not set
	SkipSettingDefaults bool

	customSchemaErrorFunc CustomSchemaErrorFunc

	// Additional schema validation options to pass through to schema validation.
	// Use this to pass document-scoped format validators or other per-validation options.
	SchemaValidationOptions []openapi3.SchemaValidationOption
}

// CustomSchemaErrorFunc allows for custom the schema error message.
type CustomSchemaErrorFunc func(err *openapi3.SchemaError) string

// WithCustomSchemaErrorFunc sets a function to override the schema error message.
// If the passed function returns an empty string, it returns to the previous Error() implementation.
func (o *Options) WithCustomSchemaErrorFunc(f CustomSchemaErrorFunc) {
	o.customSchemaErrorFunc = f
}
