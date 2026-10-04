// Package openapi3link evaluates OpenAPI 3.0 Link Objects into typed input
// plans.
//
// Evaluating a Link does not perform any I/O: it resolves the target
// operation, evaluates the Link's runtime expressions against an observed
// request/response exchange, verifies that the resulting values fit the
// target parameters and request body, and returns a [Plan]. The caller (an
// encoding layer) is then responsible for serializing the typed values,
// escaping them, selecting a content type and actually sending the request.
//
// See https://github.com/OAI/OpenAPI-Specification/blob/main/versions/3.0.3.md#link-object
package openapi3link

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Evaluate resolves link against the observed exchange and returns a typed
// input plan for the target operation.
//
// sourceURI is the URI of the document the Link belongs to; a fragment-only
// operationRef is resolved within that document. It may be empty when the
// collection contains a single document.
//
// A non-nil plan may be returned together with errors only in unusual
// situations; callers should treat a non-nil error as evaluation failure.
func (docs *Documents) Evaluate(link *openapi3.Link, ex *Exchange, sourceURI string) (*Plan, error) {
	if link == nil {
		return nil, errors.New("nil link")
	}
	if ex == nil {
		return nil, errors.New("nil exchange")
	}

	location, err := docs.resolveLinkTarget(link, sourceURI)
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		Method:      location.method,
		Path:        location.path,
		OperationID: location.operation.OperationID,
		Parameters:  make(map[string]ParameterValue),
		Server:      link.Server,
	}

	var problems []error

	keys := make([]string, 0, len(link.Parameters))
	for key := range link.Parameters {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	for _, key := range keys {
		rawValue := link.Parameters[key]
		parameter, err := resolveTargetParameter(key, location.operation)
		if err != nil {
			problems = append(problems, err)
			continue
		}

		result, err := resolveValue(rawValue, ex)
		target := "parameter " + parameter.In + "." + parameter.Name
		if err != nil {
			problems = append(problems, wrapLinkError(ScopeParameter, key, expressionString(rawValue), err))
			continue
		}
		if err := checkAssignable(result, parameterSchema(parameter), target); err != nil {
			problems = append(problems, wrapLinkError(ScopeParameter, key, expressionString(rawValue), err))
			continue
		}
		plan.Parameters[parameter.In+":"+parameter.Name] = ParameterValue{
			Parameter: parameter,
			Value:     result.value,
			IsNull:    result.isNull,
		}
	}

	// requestBody is present when the Link explicitly sets the field. JSON
	// null is a legitimate value, distinct from an omitted field.
	if link.RequestBody != nil {
		targetRequestBody := location.operation.RequestBody
		if targetRequestBody == nil || targetRequestBody.Value == nil {
			problems = append(problems, wrapLinkError(ScopeRequestBody, "", "",
				&RequestBodyNotDeclaredError{Operation: operationLabel(location)}))
		} else {
			result, err := resolveValue(link.RequestBody, ex)
			if err != nil {
				problems = append(problems, wrapLinkError(ScopeRequestBody, "", expressionString(link.RequestBody), err))
			} else if err := checkAssignable(result, requestBodySchema(targetRequestBody.Value), "request body"); err != nil {
				problems = append(problems, wrapLinkError(ScopeRequestBody, "", expressionString(link.RequestBody), err))
			} else {
				plan.RequestBody = &RequestBodyValue{
					RequestBody: targetRequestBody.Value,
					Value:       result.value,
					IsNull:      result.isNull,
				}
			}
		}
	}

	if len(problems) > 0 {
		return plan, errors.Join(problems...)
	}
	return plan, nil
}

// resolvedValue is the common outcome of resolving a literal or expression.
type resolvedValue struct {
	value  any
	isNull bool
}

// resolveValue distinguishes literals from expressions. A string beginning
// with '$' is an expression and is parsed strictly: unsupported forms fail
// instead of being treated as constants.
func resolveValue(raw any, ex *Exchange) (resolvedValue, error) {
	if isExpression(raw) {
		rawExpression := raw.(string)
		parsed, err := parseExpression(rawExpression)
		if err != nil {
			return resolvedValue{}, err
		}
		evaluated := parsed.evaluate(ex)
		if evaluated.status == statusMissing {
			return resolvedValue{}, &MissingValueError{Expression: rawExpression}
		}
		return resolvedValue{value: evaluated.value, isNull: evaluated.value == nil}, nil
	}

	// Literal. This includes strings that do not start with '$' and every
	// non-string JSON value; an explicit YAML/JSON null arrives as nil.
	return resolvedValue{value: raw, isNull: raw == nil}, nil
}

func expressionString(raw any) string {
	if isExpression(raw) {
		return raw.(string)
	}
	return ""
}

func wrapLinkError(scope ErrorScope, key, expression string, cause error) error {
	return &LinkError{Scope: scope, Key: key, Expression: expression, Cause: cause}
}

func checkAssignable(result resolvedValue, schema *openapi3.Schema, target string) error {
	if result.isNull || result.value == nil {
		if !schemaAllowsNull(schema) {
			return &NullNotAllowedError{Target: target}
		}
		return nil
	}
	if !assignable(result.value, schema) {
		return &AssignmentError{Target: target, Value: result.value}
	}
	return nil
}

// resolveTargetParameter maps a Link parameter key to exactly one declared
// target parameter. The key is either a bare unique name ("id") or a
// qualified "in.name" identity ("query.limit").
func resolveTargetParameter(key string, operation *openapi3.Operation) (*openapi3.Parameter, error) {
	label := operation.OperationID

	in := ""
	name := key
	if dot := strings.IndexByte(key, '.'); dot >= 0 {
		prefix := key[:dot]
		switch prefix {
		case openapi3.ParameterInPath, openapi3.ParameterInQuery,
			openapi3.ParameterInHeader, openapi3.ParameterInCookie:
			in = prefix
			name = key[dot+1:]
		}
	}
	if name == "" {
		return nil, &TargetParameterError{Key: key, Operation: label, Reason: "empty parameter name"}
	}

	var matches []*openapi3.Parameter
	for _, ref := range operation.Parameters {
		if ref == nil || ref.Value == nil {
			continue
		}
		parameter := ref.Value
		if parameter.Name != name {
			continue
		}
		if in != "" && parameter.In != in {
			continue
		}
		matches = append(matches, parameter)
	}

	switch {
	case len(matches) == 0:
		reason := "no declared parameter with that name"
		if in != "" {
			reason = fmt.Sprintf("no declared parameter in %q with name %q", in, name)
		}
		return nil, &TargetParameterError{Key: key, Operation: label, Reason: reason}
	case len(matches) > 1:
		locations := make([]string, 0, len(matches))
		for _, parameter := range matches {
			locations = append(locations, parameter.In+"."+parameter.Name)
		}
		return nil, &TargetParameterError{
			Key:       key,
			Operation: label,
			Reason:    "name is ambiguous, qualify it with its location: " + strings.Join(locations, ", "),
		}
	}
	return matches[0], nil
}

func parameterSchema(parameter *openapi3.Parameter) *openapi3.Schema {
	if parameter == nil {
		return nil
	}
	if parameter.Schema != nil {
		return parameter.Schema.Value
	}
	// Parameters using `content` instead of `schema`: inspect the single
	// media type schema when available.
	for _, mediaType := range parameter.Content {
		if mediaType != nil && mediaType.Schema != nil {
			return mediaType.Schema.Value
		}
	}
	return nil
}

func requestBodySchema(body *openapi3.RequestBody) *openapi3.Schema {
	if body == nil {
		return nil
	}
	// Prefer an application/json entry, then any declared media type.
	if mediaType := body.Content.Get("application/json"); mediaType != nil && mediaType.Schema != nil {
		return mediaType.Schema.Value
	}
	for _, mediaType := range body.Content {
		if mediaType != nil && mediaType.Schema != nil {
			return mediaType.Schema.Value
		}
	}
	return nil
}

func operationLabel(location operationLocation) string {
	if location.operation.OperationID != "" {
		return location.operation.OperationID
	}
	return strings.ToLower(location.method) + " " + location.path
}
