package openapi3link

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/go-openapi/jsonpointer"

	"github.com/getkin/kin-openapi/openapi3"
)

// expressionSource identifies the runtime-expression source per
// https://github.com/OAI/OpenAPI-Specification/blob/main/versions/3.0.3.md#runtime-expressions
type expressionSource string

const (
	sourceURL        expressionSource = "url"
	sourceMethod     expressionSource = "method"
	sourceStatusCode expressionSource = "statusCode"
	sourceRequest    expressionSource = "request"
	sourceResponse   expressionSource = "response"
)

// expressionPart identifies the part addressed inside a request or response.
type expressionPart string

const (
	partPath   expressionPart = "path"
	partQuery  expressionPart = "query"
	partHeader expressionPart = "header"
	partBody   expressionPart = "body"
)

// expression is a parsed OpenAPI 3.0 runtime expression.
//
// Examples of the grammar represented here:
//
//   - $url, $method, $statusCode
//   - $request.path.id
//   - $request.query.filter
//   - $request.header.X-Request-Id
//   - $request.body#/user/id
//   - $response.header.Location
//   - $response.body#/data/items/0/id
type expression struct {
	raw    string
	source expressionSource

	// part/name/pointer are set only for sourceRequest/sourceResponse.
	part expressionPart

	// name is the decoded parameter or header name for path/query/header.
	name string

	// pointer is the parsed JSON Pointer of a body fragment; nil for the
	// whole body ("$request.body" / "$response.body").
	pointer *jsonpointer.Pointer
}

// ExpressionError reports that a string claims to be a runtime expression
// (it starts with '$') but uses a form that is not supported. It is never
// returned for a plain literal.
type ExpressionError struct {
	// Expression is the rejected value.
	Expression string
	// Offset is the 0-based byte position of the first unsupported token.
	Offset int
	// Reason describes the unsupported form.
	Reason string
}

func (e *ExpressionError) Error() string {
	return fmt.Sprintf("invalid runtime expression %q at offset %d: %s", e.Expression, e.Offset, e.Reason)
}

func exprError(raw string, offset int, reason string) *ExpressionError {
	return &ExpressionError{Expression: raw, Offset: offset, Reason: reason}
}

// parseExpression parses a runtime expression.
func parseExpression(raw string) (*expression, error) {
	if raw == "" {
		return nil, exprError(raw, 0, "empty expression")
	}
	if raw[0] != '$' {
		return nil, exprError(raw, 0, "expression must start with '$'")
	}
	if raw == "$" {
		return nil, exprError(raw, 1, "missing expression source after '$'")
	}

	expr := &expression{raw: raw}
	switch {
	case raw == "$url":
		expr.source = sourceURL
	case raw == "$method":
		expr.source = sourceMethod
	case raw == "$statusCode":
		expr.source = sourceStatusCode
	case strings.HasPrefix(raw, "$request."):
		expr.source = sourceRequest
		if err := expr.parseMessage(raw[len("$request."):], len("$request.")); err != nil {
			return nil, err
		}
	case strings.HasPrefix(raw, "$response."):
		expr.source = sourceResponse
		if err := expr.parseMessage(raw[len("$response."):], len("$response.")); err != nil {
			return nil, err
		}
	default:
		// Anything starting with '$' that is not a known top-level source is
		// reported with its position rather than silently treated as a
		// constant.
		end := strings.IndexByte(raw, '.')
		if end < 0 {
			end = len(raw)
		}
		return nil, exprError(raw, 1, fmt.Sprintf("unsupported source %q", raw[1:end]))
	}
	return expr, nil
}

// parseMessage parses the remainder of a $request./$response. expression.
// start is the byte offset of remainder within the raw expression.
func (expr *expression) parseMessage(remainder string, start int) error {
	if remainder == "" {
		return exprError(expr.raw, start, "missing message part; expected path, query, header or body")
	}

	// The part name ends at the first '.' (path/query/header) or '#'
	// (body fragment). Either separator may legitimately come first for a
	// body expression such as "$request.body#/id".
	cut := len(remainder)
	if i := strings.IndexByte(remainder, '.'); i >= 0 && i < cut {
		cut = i
	}
	if i := strings.IndexByte(remainder, '#'); i >= 0 && i < cut {
		cut = i
	}
	partToken := remainder[:cut]
	tail := remainder[cut:]

	switch expressionPart(partToken) {
	case partPath, partQuery, partHeader:
		expr.part = expressionPart(partToken)
		if tail == "" || tail[0] != '.' {
			return exprError(expr.raw, start+cut, "expected '.' followed by a parameter name")
		}
		name := tail[1:]
		if i := strings.IndexByte(name, '#'); i >= 0 {
			return exprError(expr.raw, start+cut+1+i, "JSON Pointer fragment is only valid on body")
		}
		if name == "" {
			return exprError(expr.raw, start+len(tail), fmt.Sprintf("missing %s parameter name", partToken))
		}
		decoded, err := decodeFieldToken(expr.part, name)
		if err != nil {
			return exprError(expr.raw, start+len(partToken)+1, err.Error())
		}
		expr.name = decoded
	case partBody:
		expr.part = partBody
		switch {
		case tail == "":
			return nil // whole body
		case len(tail) < 2 || tail[0] != '#' || tail[1] != '/':
			return exprError(expr.raw, start+cut, "body fragment must be a JSON Pointer starting with '#/'")
		default:
			fragment := tail[1:] // strip '#'
			if err := validatePointerEscapes(fragment); err != nil {
				return exprError(expr.raw, start+cut+1, "invalid JSON Pointer: "+err.Error())
			}
			pointer, err := jsonpointer.New(fragment)
			if err != nil {
				return exprError(expr.raw, start+cut+1, "invalid JSON Pointer: "+err.Error())
			}
			expr.pointer = &pointer
		}
	default:
		return exprError(expr.raw, start, fmt.Sprintf("unsupported message part %q; expected path, query, header or body", partToken))
	}
	return nil
}

// validatePointerEscapes enforces RFC 6901 escaping: a tilde must be followed
// by '0' or '1'. github.com/go-openapi/jsonpointer accepts stray tildes, so
// the check is performed here to avoid silently treating bad expressions as
// references to oddly named properties.
func validatePointerEscapes(fragment string) error {
	for i := 0; i < len(fragment); i++ {
		if fragment[i] != '~' {
			continue
		}
		if i+1 >= len(fragment) || (fragment[i+1] != '0' && fragment[i+1] != '1') {
			return fmt.Errorf("invalid escape sequence at token %q", fragmentTokenAt(fragment, i))
		}
	}
	return nil
}

// tokenAt extracts the '/'-delimited token containing position i for error
// messages.
func fragmentTokenAt(fragment string, i int) string {
	start := strings.LastIndexByte(fragment[:i], '/') + 1
	end := len(fragment)
	if next := strings.IndexByte(fragment[i:], '/'); next >= 0 {
		end = i + next
	}
	return fragment[start:end]
}

// decodeFieldToken decodes a path/query reference token. Per the OpenAPI 3.0
// specification path and query tokens are percent-encoded; header tokens are
// matched case-insensitively at evaluation time and are taken as-is.
func decodeFieldToken(part expressionPart, token string) (string, error) {
	if part != partPath && part != partQuery {
		return token, nil
	}
	if strings.IndexByte(token, '%') < 0 {
		return token, nil
	}
	decoded, err := url.PathUnescape(token)
	if err != nil {
		return "", fmt.Errorf("invalid percent-encoding in %s token: %w", part, err)
	}
	return decoded, nil
}

// valueStatus distinguishes the outcomes of evaluating an expression.
type valueStatus int

const (
	// statusPresent: a value (possibly JSON null) was found.
	statusPresent valueStatus = iota
	// statusMissing: the referenced field or fragment does not exist.
	statusMissing
)

type evaluatedValue struct {
	value  any
	status valueStatus
}

// evaluate resolves the expression against the observed exchange. An
// explicit JSON null is returned as value=nil,status=statusPresent so it can
// be distinguished from a missing reference.
func (expr *expression) evaluate(ex *Exchange) evaluatedValue {
	switch expr.source {
	case sourceURL:
		if ex.Request.URL == nil {
			return evaluatedValue{status: statusMissing}
		}
		return evaluatedValue{value: ex.Request.URL.String(), status: statusPresent}
	case sourceMethod:
		if ex.Request.Method == "" {
			return evaluatedValue{status: statusMissing}
		}
		return evaluatedValue{value: strings.ToLower(ex.Request.Method), status: statusPresent}
	case sourceStatusCode:
		if ex.Response.StatusCode == 0 {
			return evaluatedValue{status: statusMissing}
		}
		return evaluatedValue{value: ex.Response.StatusCode, status: statusPresent}
	case sourceRequest:
		return expr.evaluateRequest(ex)
	case sourceResponse:
		return expr.evaluateResponse(ex)
	}
	return evaluatedValue{status: statusMissing}
}

func (expr *expression) evaluateRequest(ex *Exchange) evaluatedValue {
	switch expr.part {
	case partPath:
		value, ok := ex.Request.PathParams[expr.name]
		if !ok {
			return evaluatedValue{status: statusMissing}
		}
		return evaluatedValue{value: value, status: statusPresent}
	case partQuery:
		value, ok := ex.Request.QueryParams[expr.name]
		if !ok {
			return evaluatedValue{status: statusMissing}
		}
		return evaluatedValue{value: value, status: statusPresent}
	case partHeader:
		return headerValue(ex.Request.Headers, expr.name)
	case partBody:
		return bodyValue(ex.Request.Body, expr.pointer)
	}
	return evaluatedValue{status: statusMissing}
}

func (expr *expression) evaluateResponse(ex *Exchange) evaluatedValue {
	switch expr.part {
	case partHeader:
		return headerValue(ex.Response.Headers, expr.name)
	case partBody:
		return bodyValue(ex.Response.Body, expr.pointer)
	}
	// OpenAPI 3.0 responses expose only header and body: a well-formed
	// expression cannot reach this branch.
	return evaluatedValue{status: statusMissing}
}

// headerValue performs a case-insensitive header lookup. Go's http.Header
// canonicalizes keys, but callers may supply raw maps, so the lookup is
// forgiving on both sides.
func headerValue(headers map[string]string, name string) evaluatedValue {
	if len(headers) == 0 {
		return evaluatedValue{status: statusMissing}
	}
	if value, ok := headers[name]; ok {
		return evaluatedValue{value: value, status: statusPresent}
	}
	lower := strings.ToLower(name)
	for key, value := range headers {
		if strings.ToLower(key) == lower {
			return evaluatedValue{value: value, status: statusPresent}
		}
	}
	return evaluatedValue{status: statusMissing}
}

// bodyValue resolves an optional JSON Pointer against a decoded body. A nil
// pointer means the whole body is referenced.
func bodyValue(body any, pointer *jsonpointer.Pointer) evaluatedValue {
	if pointer == nil {
		if body == nil {
			return evaluatedValue{status: statusMissing}
		}
		return evaluatedValue{value: body, status: statusPresent}
	}
	if body == nil {
		return evaluatedValue{status: statusMissing}
	}
	value, _, err := pointer.Get(body)
	if err != nil {
		return evaluatedValue{status: statusMissing}
	}
	if value == nil {
		return evaluatedValue{value: nil, status: statusPresent}
	}
	// jsonpointer may return typed nil pointers; treat those as present null.
	if reflect.ValueOf(value).Kind() == reflect.Pointer && reflect.ValueOf(value).IsNil() {
		return evaluatedValue{value: nil, status: statusPresent}
	}
	return evaluatedValue{value: value, status: statusPresent}
}

// isExpression reports whether a link value claims to be a runtime
// expression. Only strings beginning with '$' are expressions; every other
// JSON value is a literal.
func isExpression(value any) bool {
	raw, ok := value.(string)
	return ok && len(raw) > 0 && raw[0] == '$'
}

// assignable checks whether value can be assigned to a parameter or request
// body described by schema. A nil schema means "unconstrained".
func assignable(value any, schema *openapi3.Schema) bool {
	if schema == nil {
		return true
	}
	if value == nil {
		return schemaAllowsNull(schema)
	}
	return jsonValueMatchesSchema(value, schema)
}
