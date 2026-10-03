package openapi3pairwise

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
)

// Case is one complete typed request case: a candidate for every planned
// parameter, keyed by parameter name.
type Case struct {
	Values map[string]Candidate
}

// key returns a canonical, deterministic identity for the case.
func (c Case) key(names []string) string {
	var sb strings.Builder
	for _, name := range names {
		sb.WriteString(name)
		sb.WriteByte('=')
		sb.WriteString(c.Values[name].key())
		sb.WriteByte('\x00')
	}
	return sb.String()
}

// encodeScalar renders a concrete scalar candidate value for the wire.
func encodeScalar(v any) (string, error) {
	switch value := v.(type) {
	case string:
		return value, nil
	case bool:
		return strconv.FormatBool(value), nil
	case int:
		return strconv.Itoa(value), nil
	case int8:
		return strconv.FormatInt(int64(value), 10), nil
	case int16:
		return strconv.FormatInt(int64(value), 10), nil
	case int32:
		return strconv.FormatInt(int64(value), 10), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case uint:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(value), 10), nil
	case uint64:
		return strconv.FormatUint(value, 10), nil
	case float32:
		return strconv.FormatFloat(float64(value), 'g', -1, 32), nil
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), nil
	}
	return "", fmt.Errorf("openapi3pairwise: cannot encode candidate value of type %T", v)
}

// BuildRequest assembles an in-memory HTTP request for the case together
// with a ready-to-use validation input. Nothing is sent over the network.
//
// Absent candidates are omitted. Null candidates are rendered as present
// empty values; whether that validates depends on the parameter schema and
// its allowEmptyValue/nullable settings.
func (c Case) BuildRequest(route *routers.Route) (*openapi3filter.RequestValidationInput, error) {
	params := parametersByName(route)

	query := url.Values{}
	header := http.Header{}
	pathParams := map[string]string{}
	var cookies []*http.Cookie

	for _, name := range sortedKeys(c.Values) {
		cand := c.Values[name]
		param, ok := params[name]
		if !ok {
			return nil, fmt.Errorf("openapi3pairwise: case refers to unknown parameter %q", name)
		}
		if cand.Kind == ValueAbsent {
			continue
		}
		encoded := ""
		if cand.Kind == ValueConcrete {
			var err error
			if encoded, err = encodeScalar(cand.Value); err != nil {
				return nil, err
			}
		}
		switch param.In {
		case openapi3.ParameterInPath:
			pathParams[name] = encoded
		case openapi3.ParameterInQuery:
			query.Add(name, encoded)
		case openapi3.ParameterInHeader:
			header.Set(name, encoded)
		case openapi3.ParameterInCookie:
			cookies = append(cookies, &http.Cookie{Name: name, Value: encoded})
		default:
			return nil, fmt.Errorf("openapi3pairwise: unsupported parameter location %q", param.In)
		}
	}

	method := route.Method
	if method == "" {
		method = http.MethodGet
	}
	target := "http://openapi3pairwise.invalid" + route.Path
	if raw := query.Encode(); raw != "" {
		target += "?" + raw
	}
	req := httptest.NewRequest(method, target, nil)
	req.Header = header
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}

	return &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
	}, nil
}

// parametersOf returns the effective parameters of the route's operation:
// path item parameters overridden by operation parameters, like ValidateRequest.
func parametersOf(route *routers.Route) []*openapi3.Parameter {
	var out []*openapi3.Parameter
	operationParams := route.Operation.Parameters
	if route.PathItem != nil {
		for _, ref := range route.PathItem.Parameters {
			param := ref.Value
			if param == nil {
				continue
			}
			if operationParams != nil && operationParams.GetByInAndName(param.In, param.Name) != nil {
				continue
			}
			out = append(out, param)
		}
	}
	for _, ref := range operationParams {
		if ref.Value != nil {
			out = append(out, ref.Value)
		}
	}
	return out
}

func parametersByName(route *routers.Route) map[string]*openapi3.Parameter {
	out := map[string]*openapi3.Parameter{}
	for _, param := range parametersOf(route) {
		if _, ok := out[param.Name]; !ok {
			out[param.Name] = param
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
