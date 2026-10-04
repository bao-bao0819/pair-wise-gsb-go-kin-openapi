package openapi3link

import (
	"net/url"

	"github.com/getkin/kin-openapi/openapi3"
)

// Exchange is the observed request/response pair a Link is evaluated against.
// It contains decoded values only: bodies must be JSON-decoded into Go values
// so body fragments can be resolved with JSON Pointer.
//
// This type is unrelated to net/http message bodies: the evaluator performs
// no I/O and never sends a request.
type Exchange struct {
	// Request describes the request that produced the response carrying the
	// Link. All fields are optional and only required for expressions that
	// reference them ($url, $method, $request.*).
	Request RequestData

	// Response describes the response carrying the Link. $statusCode,
	// $response.header.* and $response.body[#/...] draw from here.
	Response ResponseData
}

// RequestData is the decoded request side of an Exchange.
type RequestData struct {
	// Method is the HTTP method in upper case (as used by net/http).
	// $method evaluates to its lower-case form.
	Method string

	// URL is the full request URL and backs $url.
	URL *url.URL

	// PathParams holds decoded path parameter values keyed by the parameter
	// names declared in the source operation, backing $request.path.*.
	PathParams map[string]string

	// QueryParams holds decoded query parameter values backing
	// $request.query.*. Values are typically string or []string; values taken
	// from a JSON-decoded body may be arbitrary JSON values.
	QueryParams map[string]any

	// Headers backs $request.header.*. Lookup is case-insensitive.
	Headers map[string]string

	// Body is the JSON-decoded request body backing $request.body[#/...].
	Body any
}

// ResponseData is the decoded response side of an Exchange.
type ResponseData struct {
	// StatusCode backs $statusCode.
	StatusCode int

	// Headers backs $response.header.*. Lookup is case-insensitive.
	Headers map[string]string

	// Body is the JSON-decoded response body backing $response.body[#/...].
	Body any
}

// ParameterValue is one typed input for the target operation. The encoding
// layer is responsible for serialization (style/explode/content-type), URL
// escaping and, for non-string parameters, string conversion.
type ParameterValue struct {
	// Parameter is the resolved target parameter declaration.
	Parameter *openapi3.Parameter

	// Value is the typed, unencoded value. A nil value with IsNull set means
	// the expression explicitly produced JSON null; IsNull false with a nil
	// Value never occurs in a successfully produced plan.
	Value any

	// IsNull reports that Value is an explicit JSON null (as opposed to a
	// missing reference, which is reported as an error).
	IsNull bool
}

// RequestBodyValue is the typed request body prepared for the target
// operation. Content selection, JSON marshaling and framing are left to the
// encoding layer.
type RequestBodyValue struct {
	// RequestBody is the target operation's requestBody declaration.
	RequestBody *openapi3.RequestBody

	// Value is the decoded (literal or expression-produced) body value.
	Value any

	// IsNull reports an explicit JSON null, analogously to ParameterValue.
	IsNull bool
}

// Plan is the result of evaluating a Link: everything needed to construct the
// next request, but nothing encoded and no network activity.
type Plan struct {
	// Method is the target operation's HTTP method in upper case.
	Method string

	// Path is the target path template as declared in the document
	// (e.g. "/users/{id}").
	Path string

	// OperationID is the target operation's operationId when known.
	OperationID string

	// Parameters holds the inputs named by the Link, keyed by "in:name"
	// (e.g. "path:id"). Only parameters referenced by the Link appear here.
	Parameters map[string]ParameterValue

	// RequestBody is non-nil when the Link supplies a requestBody.
	RequestBody *RequestBodyValue

	// Server is the Link's optional server override.
	Server *openapi3.Server
}

// Parameter returns the resolved value for the parameter with the given
// location and name, or nil if the Link did not reference it.
func (plan *Plan) Parameter(in, name string) *ParameterValue {
	if plan == nil {
		return nil
	}
	if value, ok := plan.Parameters[in+":"+name]; ok {
		result := value
		return &result
	}
	return nil
}
