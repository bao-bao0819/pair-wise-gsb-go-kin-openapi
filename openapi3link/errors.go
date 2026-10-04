package openapi3link

import "fmt"

// ErrorScope identifies where in a Link an error occurred.
type ErrorScope string

const (
	// ScopeParameter: an entry of the Link's parameters map.
	ScopeParameter ErrorScope = "parameter"
	// ScopeRequestBody: the Link's requestBody field.
	ScopeRequestBody ErrorScope = "requestBody"
)

// LinkError wraps an error with the Link location it applies to.
type LinkError struct {
	// Scope is ScopeParameter or ScopeRequestBody.
	Scope ErrorScope

	// Key is the parameters-map key for ScopeParameter ("id" or "query.limit");
	// it is empty for ScopeRequestBody.
	Key string

	// Expression is the runtime expression being processed, empty for literals.
	Expression string

	// Cause is the underlying error, for example *ExpressionError,
	// *MissingValueError, *NullNotAllowedError or *AssignmentError.
	Cause error
}

func (e *LinkError) Error() string {
	where := string(e.Scope)
	if e.Scope == ScopeParameter {
		where = fmt.Sprintf("parameters[%q]", e.Key)
	}
	if e.Expression != "" {
		return fmt.Sprintf("link %s (expression %q): %v", where, e.Expression, e.Cause)
	}
	return fmt.Sprintf("link %s: %v", where, e.Cause)
}

func (e *LinkError) Unwrap() error { return e.Cause }

// MissingValueError reports that an expression referenced a field or body
// fragment that is absent in the exchange.
type MissingValueError struct {
	Expression string
}

func (e *MissingValueError) Error() string {
	return fmt.Sprintf("expression %q resolves to no value", e.Expression)
}

// NullNotAllowedError reports that an expression resolved to an explicit JSON
// null but the target parameter or request body does not allow null.
type NullNotAllowedError struct {
	Expression string
	Target     string
}

func (e *NullNotAllowedError) Error() string {
	if e.Expression == "" {
		return fmt.Sprintf("explicit null is not assignable to %s", e.Target)
	}
	return fmt.Sprintf("expression %q resolved to null but %s does not allow null", e.Expression, e.Target)
}

// AssignmentError reports that a value lies outside the target's assignable
// range (wrong JSON type or enum mismatch). No conversion is attempted: the
// evaluator never guesses how to coerce a value.
type AssignmentError struct {
	Expression string
	Target     string
	Value      any
}

func (e *AssignmentError) Error() string {
	if e.Expression == "" {
		return fmt.Sprintf("literal value %s is not assignable to %s", valueDescription(e.Value), e.Target)
	}
	return fmt.Sprintf("expression %q produced %s, not assignable to %s", e.Expression, valueDescription(e.Value), e.Target)
}

// TargetParameterError reports that a Link parameter key does not identify a
// parameter of the target operation. The key must match a declared name/in
// pair, either as "name" (when unique) or "in.name".
type TargetParameterError struct {
	Key       string
	Operation string
	Reason    string
}

func (e *TargetParameterError) Error() string {
	return fmt.Sprintf("link parameter %q does not identify a parameter of operation %q: %s", e.Key, e.Operation, e.Reason)
}

// RequestBodyNotDeclaredError reports that a Link supplies a requestBody for
// an operation that declares none.
type RequestBodyNotDeclaredError struct {
	Operation string
}

func (e *RequestBodyNotDeclaredError) Error() string {
	return fmt.Sprintf("target operation %q declares no requestBody", e.Operation)
}

func valueDescription(value any) string {
	return fmt.Sprintf("%v (%T)", value, value)
}
