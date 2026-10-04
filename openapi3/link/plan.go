package link

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Plan is the typed input plan for a target operation. It is ready for an
// encoding layer: parameter values are never spliced into a URL by this
// package.
type Plan struct {
	// Target is the resolved operation.
	Target Target

	// Parameters holds one entry per Link.Parameters key that resolved.
	Parameters []ParamValue

	// HasBody reports whether Link.RequestBody produced a request body.
	HasBody bool
	// Body is the evaluated request body value.
	Body any

	// Server is Link.Server when present, else nil. The server URL template
	// and its variables are left untouched for the caller.
	Server *openapi3.Server
}

// ParamValue is one target parameter value with its explicit name/in
// identity.
type ParamValue struct {
	Name  string
	In    string
	Value any
	// Null distinguishes an evaluated explicit JSON null from an absent
	// value.
	Null bool
}

// Source identifies the document a Link belongs to. DocURL indexes
// Documents; Document scopes operationId resolution and relative
// operationRef values. At least one is required when the link uses
// operationId or a document-local operationRef.
type Source struct {
	Document *openapi3.T
	DocURL   string
}

// Evaluate evaluates a Link Object against an observed exchange and returns
// a typed input plan. It never sends a network request.
//
// Literals (numbers, booleans, objects, and strings not starting with "$")
// are passed through as constants. Strings starting with "$" are runtime
// expressions: invalid expressions are reported with their position rather
// than silently treated as constants.
func Evaluate(docs *Documents, source Source, link *openapi3.Link, ex *Exchange) (*Plan, error) {
	if docs == nil {
		return nil, errors.New("openapi3/link: Documents are required")
	}
	if link == nil {
		return nil, errors.New("openapi3/link: Link is required")
	}
	if ex == nil {
		ex = &Exchange{}
	}

	target, err := resolveTarget(docs, source, link)
	if err != nil {
		return nil, err
	}

	env := &evalContext{req: ex.Request, res: ex.Response}

	plan := &Plan{Target: *target, Server: link.Server}
	var errs []error

	effectiveParams := mergeParameters(target)

	keys := make([]string, 0, len(link.Parameters))
	for key := range link.Parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		at := Location{ParamKey: key}
		param, reason := resolveParameterIdentity(key, effectiveParams)
		if param == nil {
			errs = append(errs, &UnassignableError{At: at, Reason: reason})
			continue
		}

		value, null, evalErr := evalValue(link.Parameters[key], at, env)
		if evalErr != nil {
			errs = append(errs, evalErr)
			continue
		}
		plan.Parameters = append(plan.Parameters, ParamValue{
			Name:  param.Name,
			In:    param.In,
			Value: value,
			Null:  null,
		})
	}

	if link.RequestBody != nil {
		at := Location{}
		if target.Operation.RequestBody == nil {
			errs = append(errs, &UnassignableError{At: at, Reason: "target operation declares no request body"})
		} else {
			body, _, evalErr := evalValue(link.RequestBody, at, env)
			if evalErr != nil {
				errs = append(errs, evalErr)
			} else {
				plan.HasBody = true
				plan.Body = body
			}
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return plan, nil
}

func resolveTarget(docs *Documents, source Source, link *openapi3.Link) (*Target, error) {
	switch {
	case link.OperationID != "" && link.OperationRef != "":
		return nil, errors.New("openapi3/link: operationId and operationRef are mutually exclusive")
	case link.OperationRef != "":
		return docs.resolveOperationRef(source.Document, source.DocURL, link.OperationRef)
	case link.OperationID != "":
		if source.Document == nil && source.DocURL == "" {
			return nil, &TargetError{OperationID: link.OperationID, Reason: "no source document supplied to scope operationId"}
		}
		doc := source.Document
		if doc == nil {
			doc = docs.byURL[source.DocURL]
		}
		if doc == nil {
			return nil, &TargetError{OperationID: link.OperationID, Reason: "source document is not loaded"}
		}
		return docs.resolveOperationID(doc, link.OperationID)
	default:
		return nil, errors.New("openapi3/link: link requires operationId or operationRef")
	}
}

// mergeParameters combines path-item level and operation level parameters.
// Operation-level parameters override path-item parameters with the same
// name/in identity.
func mergeParameters(target *Target) []*openapi3.Parameter {
	var out []*openapi3.Parameter
	index := make(map[string]int)
	add := func(refs openapi3.Parameters) {
		for _, ref := range refs {
			if ref == nil || ref.Value == nil {
				continue
			}
			p := ref.Value
			id := p.In + ":" + p.Name
			if i, ok := index[id]; ok {
				out[i] = p
				continue
			}
			index[id] = len(out)
			out = append(out, p)
		}
	}
	if target.PathItem != nil {
		add(target.PathItem.Parameters)
	}
	if target.Operation != nil {
		add(target.Operation.Parameters)
	}
	return out
}

var validIn = map[string]bool{
	openapi3.ParameterInPath:   true,
	openapi3.ParameterInQuery:  true,
	openapi3.ParameterInHeader: true,
	openapi3.ParameterInCookie: true,
}

// resolveParameterIdentity verifies that a Link parameter key identifies
// exactly one declared target parameter.
func resolveParameterIdentity(key string, declared []*openapi3.Parameter) (*openapi3.Parameter, string) {
	var in, name string
	if i := strings.IndexByte(key, '.'); i >= 0 {
		in, name = key[:i], key[i+1:]
		if !validIn[in] {
			return nil, fmt.Sprintf("unknown parameter location %q (expected path, query, header or cookie)", in)
		}
		if name == "" {
			return nil, fmt.Sprintf("qualified key %q is missing a parameter name", key)
		}
		for _, p := range declared {
			if p.In == in && p.Name == name {
				return p, ""
			}
		}
		return nil, fmt.Sprintf("target operation declares no %s parameter named %q", in, name)
	}

	name = key
	var matches []*openapi3.Parameter
	for _, p := range declared {
		if p.Name == name {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Sprintf("target operation declares no parameter named %q; qualify it as path.%[1]s, query.%[1]s, header.%[1]s or cookie.%[1]s", name)
	case 1:
		return matches[0], ""
	default:
		locs := make([]string, len(matches))
		for i, p := range matches {
			locs[i] = p.In + "." + p.Name
		}
		sort.Strings(locs)
		return nil, fmt.Sprintf("parameter name %q is ambiguous; qualify the key (%s)", name, strings.Join(locs, ", "))
	}
}

// evalValue evaluates one declared value. Arrays and objects are traversed:
// any embedded string starting with "$" is an unsupported expression form
// for that position and is reported with a subpath, not treated as a
// constant.
func evalValue(v any, at Location, env *evalContext) (result any, null bool, err error) {
	switch t := v.(type) {
	case nil:
		return nil, true, nil
	case string:
		if strings.HasPrefix(t, "$") {
			return evalExpression(t, at, env)
		}
		return t, false, nil
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			sub := at
			sub.Sub = at.Sub + fmt.Sprintf("[%d]", i)
			val, isNull, evalErr := evalValue(item, sub, env)
			if evalErr != nil {
				return nil, false, evalErr
			}
			out[i] = nullOrValue(val, isNull)
		}
		return out, false, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for _, k := range sortedMapKeys(t) {
			sub := at
			sub.Sub = at.Sub + "." + k
			val, isNull, evalErr := evalValue(t[k], sub, env)
			if evalErr != nil {
				return nil, false, evalErr
			}
			out[k] = nullOrValue(val, isNull)
		}
		return out, false, nil
	default:
		return v, false, nil
	}
}

func nullOrValue(v any, isNull bool) any {
	if isNull {
		return nil
	}
	return v
}

func evalExpression(raw string, at Location, env *evalContext) (any, bool, error) {
	parsed, parseErr := parseExpression(raw)
	if parseErr != nil {
		parseErr.At = at
		return nil, false, parseErr
	}
	v, evalErr := parsed.eval(env)
	if evalErr != nil {
		if errors.Is(evalErr, errMissing) {
			return nil, false, &MissingError{Expr: raw, At: at, Reason: missingReason(parsed)}
		}
		pe := &ExprError{Expr: raw, At: at, Reason: evalErr.Error()}
		return nil, false, pe
	}
	if v == nil {
		return nil, true, nil
	}
	return v, false, nil
}

// EvaluateValue evaluates a single standalone runtime expression against an
// exchange. It is useful for callers evaluating Link values directly. A
// returned null value is reported with null == true.
func EvaluateValue(expression string, ex *Exchange) (value any, null bool, err error) {
	if ex == nil {
		ex = &Exchange{}
	}
	return evalExpression(expression, Location{}, &evalContext{req: ex.Request, res: ex.Response})
}

func missingReason(x expr) string {
	switch x.root {
	case "url":
		return "request URL is not available"
	case "method":
		return "request method is not available"
	case "statusCode":
		return "response is not available"
	}
	if x.part == "body" {
		if len(x.pointer) == 0 {
			return x.root + " body is not available"
		}
		return x.root + " body has no JSON Pointer target /" + strings.Join(x.pointer, "/")
	}
	return x.root + " has no " + x.part + " value named " + strconv.Quote(x.lookupName())
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
