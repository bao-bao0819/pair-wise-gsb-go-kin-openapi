package openapi3plan

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// buildRequest serializes one complete assignment into an *http.Request that
// openapi3filter can validate. Only primitive parameter values are supported;
// styles must be the defaults or other styles the existing decoder understands
// for primitives.
//
// Absent parameters are omitted entirely. Null parameters are emitted as an
// empty wire segment (query: "name="), which the standard decoder turns into a
// nil value for non-string primitive schemas; whether that is accepted is left
// to the ordinary parameter validation, exactly as for a real request.
func buildRequest(method, pathPattern string, domains []*paramDomain, a assignment) (*http.Request, map[string]Candidate, error) {
	values := make(map[string]Candidate, len(domains))

	pathValues := make(map[string]string)
	query := make(url.Values)
	headers := make(map[string]string)
	type cookie struct{ name, value string }
	var cookies []cookie

	for di, d := range domains {
		c := d.candidates[a[di]]
		values[d.param.Name] = c
		param := d.param

		if c.Kind == KindAbsent {
			continue
		}

		raw := ""
		if c.Kind == KindValue {
			r, err := encodeScalar(c.Value)
			if err != nil {
				return nil, nil, fmt.Errorf("parameter %q: %w", param.Name, err)
			}
			raw = r
		}

		switch param.In {
		case openapi3.ParameterInPath:
			pathValues[param.Name] = encodePath(param, raw)
		case openapi3.ParameterInQuery:
			if err := encodeQuery(param, raw, query); err != nil {
				return nil, nil, err
			}
		case openapi3.ParameterInHeader:
			style := param.Style
			if style == "" {
				style = openapi3.SerializationSimple
			}
			if style != openapi3.SerializationSimple {
				return nil, nil, fmt.Errorf("parameter %q: header style %q is not supported by the planner", param.Name, style)
			}
			headers[param.Name] = raw
		case openapi3.ParameterInCookie:
			style := param.Style
			if style == "" {
				style = openapi3.SerializationForm
			}
			if style != openapi3.SerializationForm {
				return nil, nil, fmt.Errorf("parameter %q: cookie style %q is not supported by the planner", param.Name, style)
			}
			cookies = append(cookies, cookie{param.Name, raw})
		default:
			return nil, nil, fmt.Errorf("parameter %q: unsupported location %q", param.Name, param.In)
		}
	}

	// Substitute path parameters into the operation's path template.
	finalPath := substitutePath(pathPattern, pathValues)
	requestURL := "http://example.com" + finalPath
	if encoded := query.Encode(); encoded != "" {
		requestURL += "?" + encoded
	}

	req, err := http.NewRequest(method, requestURL, nil)
	if err != nil {
		return nil, nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	for _, ck := range cookies {
		req.AddCookie(&http.Cookie{Name: ck.name, Value: ck.value})
	}
	return req, values, nil
}

// encodeScalar renders a primitive the same way fmt/strconv do on the wire.
func encodeScalar(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case int:
		return strconv.FormatInt(int64(t), 10), nil
	case int8:
		return strconv.FormatInt(int64(t), 10), nil
	case int16:
		return strconv.FormatInt(int64(t), 10), nil
	case int32:
		return strconv.FormatInt(int64(t), 10), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case uint:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint64:
		return strconv.FormatUint(t, 10), nil
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 32), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported value type %T (only primitives are supported)", v)
	}
}

func encodePath(param *openapi3.Parameter, raw string) string {
	style := param.Style
	if style == "" {
		style = openapi3.SerializationSimple
	}
	switch style {
	case openapi3.SerializationSimple:
		return raw
	case openapi3.SerializationLabel:
		return "." + raw
	case openapi3.SerializationMatrix:
		return ";" + param.Name + "=" + raw
	default:
		// Unknown styles are rejected later by validation; emit raw so the
		// failure surfaces through the ordinary validation rules.
		return raw
	}
}

func encodeQuery(param *openapi3.Parameter, raw string, query url.Values) error {
	style := param.Style
	if style == "" {
		style = openapi3.SerializationForm
	}
	if style != openapi3.SerializationForm {
		return fmt.Errorf("parameter %q: query style %q is not supported by the planner", param.Name, style)
	}
	// Primitives are unaffected by explode; arrays are out of scope. net/url
	// performs percent-encoding when the query string is rendered.
	query.Add(param.Name, raw)
	return nil
}

// substitutePath replaces {name} occurrences with encoded path values. Path
// parameters are required by OpenAPI; a missing substitution is left as-is so
// ordinary validation reports the required error.
func substitutePath(pattern string, values map[string]string) string {
	out := pattern
	for name, value := range values {
		out = strings.ReplaceAll(out, "{"+name+"}", value)
	}
	return out
}

// reqPathParams renders present path parameters for RequestValidationInput.
func reqPathParams(domains []*paramDomain, a assignment) map[string]string {
	out := make(map[string]string)
	for di, d := range domains {
		if d.param.In != openapi3.ParameterInPath {
			continue
		}
		c := d.candidates[a[di]]
		if c.Kind == KindAbsent {
			continue
		}
		raw := ""
		if c.Kind == KindValue {
			raw, _ = encodeScalar(c.Value)
		}
		out[d.param.Name] = encodePath(d.param, raw)
	}
	return out
}
