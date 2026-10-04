package link

import (
	"fmt"
	"strconv"
)

// Location identifies a place inside a Link Object where an input value is
// declared.
//
// See https://github.com/OAI/OpenAPI-Specification/blob/main/versions/3.0.3.md#link-object
type Location struct {
	// ParamKey is the key in Link.Parameters. It is empty when the value
	// comes from Link.RequestBody.
	ParamKey string
	// Sub is a JSON-ish subpath inside a constant value, e.g. "[0]" or
	// ".name". It is empty for the value itself.
	Sub string
}

func (l Location) String() string {
	if l.ParamKey != "" {
		return "link.parameters[" + strconv.Quote(l.ParamKey) + "]" + l.Sub
	}
	return "link.requestBody" + l.Sub
}

// ExprError reports a runtime expression that is syntactically invalid or
// uses a form not supported by OpenAPI 3.0. Pos is the byte offset inside
// Expr where the problem was detected.
type ExprError struct {
	Expr   string
	At     Location
	Pos    int
	Reason string
}

func (e *ExprError) Error() string {
	return fmt.Sprintf("openapi3/link: invalid runtime expression %q at offset %d (%s) at %s",
		e.Expr, e.Pos, e.Reason, e.At)
}

// MissingError reports that a syntactically valid expression references a
// value that is absent in the observed exchange (a missing header, query
// parameter, path parameter, or JSON Pointer target).
type MissingError struct {
	Expr   string
	At     Location
	Reason string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("openapi3/link: runtime expression %q has no value (%s) at %s",
		e.Expr, e.Reason, e.At)
}

// TargetError reports that a Link Object cannot be resolved to a target
// operation through its operationId or operationRef.
type TargetError struct {
	OperationID  string
	OperationRef string
	Reason       string
}

func (e *TargetError) Error() string {
	switch {
	case e.OperationRef != "":
		return fmt.Sprintf("openapi3/link: cannot resolve operationRef %q: %s", e.OperationRef, e.Reason)
	default:
		return fmt.Sprintf("openapi3/link: cannot resolve operationId %q: %s", e.OperationID, e.Reason)
	}
}

// UnassignableError reports that a Link parameter cannot be assigned to the
// target operation: the declared identity is unknown, ambiguous, or the
// target has no request body to receive Link.RequestBody.
type UnassignableError struct {
	At     Location
	Reason string
}

func (e *UnassignableError) Error() string {
	return fmt.Sprintf("openapi3/link: value at %s cannot be assigned: %s", e.At, e.Reason)
}

// DuplicateOperationIDError reports that operationId values are not unique
// inside a single loaded document.
type DuplicateOperationIDError struct {
	DocURL       string
	ID           string
	FirstTarget  string
	SecondTarget string
}

func (e *DuplicateOperationIDError) Error() string {
	return fmt.Sprintf("openapi3/link: duplicate operationId %q in document %q: %s and %s",
		e.ID, e.DocURL, e.FirstTarget, e.SecondTarget)
}
