package openapi3filter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// EncodedParameter is a value of an OpenAPI parameter serialized according to
// the parameter's style and explode properties.
//
// The fields are populated according to the parameter's location ("in"):
//   - path parameters: PathSegment is the value (including the style prefix
//     such as "." or ";name=") to substitute for the "{name}" template in the
//     URL path.
//   - query parameters: Query is the encoded key=value pairs to append to the
//     URL query component, in their serialization order.
//   - header parameters: HeaderName and HeaderValue describe the HTTP header.
//   - cookie parameters: CookieName and CookieValue describe the Cookie value.
//
// The strings are already percent-escaped where the location requires it and
// must not be escaped again.
type EncodedParameter struct {
	// In is the parameter's location: "path", "query", "header", or "cookie".
	In string
	// Name is the parameter's name.
	Name string

	// PathSegment is the serialized value of a path parameter.
	PathSegment string

	// Query is the serialized key=value pairs of a query parameter, in the
	// order they must appear in the URL query component.
	Query []string

	// HeaderName and HeaderValue are the serialized name and value of a
	// header parameter.
	HeaderName  string
	HeaderValue string

	// CookieName and CookieValue are the serialized name and value of a
	// cookie parameter.
	CookieName  string
	CookieValue string
}

// EncodeParameter serializes value according to the serialization method
// (style/explode/allowReserved) defined by the OpenAPI parameter.
//
// value must be a primitive (string, bool, integer or float), a slice or
// array of primitives, or a map with string keys and primitive values. Nested
// objects, nested arrays and null values are rejected because the OpenAPI
// parameter serialization styles cannot represent them unambiguously.
//
// The function returns a *ParseError when value does not match the
// parameter's serialization method, the style/location combination is not
// allowed by OpenAPI, or value cannot be represented without information
// loss (e.g. an empty array in a header, a nested object with deepObject, or
// a value containing a structural delimiter that the location cannot
// distinguish from a separator).
func EncodeParameter(param *openapi3.Parameter, value any) (*EncodedParameter, error) {
	if param == nil {
		return nil, fmt.Errorf("parameter is not defined")
	}
	switch param.In {
	case openapi3.ParameterInPath, openapi3.ParameterInQuery,
		openapi3.ParameterInHeader, openapi3.ParameterInCookie:
	default:
		return nil, parameterEncodingError(param, fmt.Errorf("unsupported parameter's 'in': %q", param.In))
	}
	sm, err := param.SerializationMethod()
	if err != nil {
		return nil, parameterEncodingError(param, err)
	}

	kind, values, props, err := normalizeValue(value)
	if err != nil {
		return nil, parameterEncodingError(param, err)
	}

	out := &EncodedParameter{In: param.In, Name: param.Name}
	switch param.In {
	case openapi3.ParameterInPath:
		err = encodePath(out, param, sm, kind, values, props)
	case openapi3.ParameterInQuery:
		err = encodeQuery(out, param, sm, kind, values, props)
	case openapi3.ParameterInHeader:
		err = encodeHeader(out, param, sm, kind, values, props)
	case openapi3.ParameterInCookie:
		err = encodeCookie(out, param, sm, kind, values, props)
	}
	if err != nil {
		return nil, parameterEncodingError(param, err)
	}
	return out, nil
}

// SetOnRequest writes the serialized parameter into req:
//   - path parameters are stored in pathParams (the caller substitutes the
//     segments into the request URL's path template); if pathParams is nil a
//     new map is allocated and returned.
//   - query parameters are merged into the existing query string, preserving
//     the order and occurrences of unrelated query parameters; previous
//     occurrences of this parameter's key are replaced.
//   - header parameters set the corresponding HTTP header.
//   - cookie parameters are added to the request's Cookie header.
//
// The parameter definition is never modified.
func (e *EncodedParameter) SetOnRequest(req *http.Request, pathParams map[string]string) (map[string]string, error) {
	if req == nil {
		return pathParams, fmt.Errorf("request is not defined")
	}
	switch e.In {
	case openapi3.ParameterInPath:
		if pathParams == nil {
			pathParams = make(map[string]string)
		}
		pathParams[e.Name] = e.PathSegment
	case openapi3.ParameterInQuery:
		setEncodedQuery(req, e.Name, e.Query)
	case openapi3.ParameterInHeader:
		req.Header.Set(e.HeaderName, e.HeaderValue)
	case openapi3.ParameterInCookie:
		req.AddCookie(&http.Cookie{Name: e.CookieName, Value: e.CookieValue})
	default:
		return pathParams, fmt.Errorf("unsupported parameter's location: %q", e.In)
	}
	return pathParams, nil
}

type valueKind int

const (
	kindPrimitive valueKind = iota
	kindArray
	kindObject
)

// OrderedObjectValue is implemented by object values whose properties carry a
// meaningful serialization order. The returned [key, value] pairs must be
// primitive strings and are serialized in the given order. Values that are
// plain map[string]any are serialized with keys sorted lexicographically.
type OrderedObjectValue interface {
	OrderedObjectValue() [][2]string
}

// normalizeValue converts the typed value into a primitive string, a slice of
// primitive strings, or sorted [key,value] pairs, rejecting values the
// parameter styles cannot represent (null, nested objects, nested arrays).
func normalizeValue(value any) (kind valueKind, values []string, props [][2]string, err error) {
	if value == nil {
		return kindPrimitive, nil, nil, fmt.Errorf("a null value cannot be encoded")
	}
	if ordered, ok := value.(OrderedObjectValue); ok {
		pairs := ordered.OrderedObjectValue()
		return kindObject, nil, pairs, validateOrderedProps(pairs)
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return kindPrimitive, nil, nil, fmt.Errorf("a null value cannot be encoded")
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if rv.Type() == reflect.TypeOf(json.RawMessage(nil)) {
			break
		}
		kind = kindArray
		values = make([]string, rv.Len())
		for i := range rv.Len() {
			s, perr := primitiveToString(rv.Index(i).Interface())
			if perr != nil {
				return 0, nil, nil, fmt.Errorf("array item %d: %w", i, perr)
			}
			values[i] = s
		}
		return kind, values, nil, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return 0, nil, nil, fmt.Errorf("object keys must be strings")
		}
		kind = kindObject
		keys := make([]string, 0, rv.Len())
		byKey := make(map[string]string, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			s, perr := primitiveToString(iter.Value().Interface())
			if perr != nil {
				return 0, nil, nil, fmt.Errorf("object property %q: %w", key, perr)
			}
			if _, dup := byKey[key]; dup {
				return 0, nil, nil, fmt.Errorf("object property %q is duplicated", key)
			}
			byKey[key] = s
			keys = append(keys, key)
		}
		sort.Strings(keys)
		props = make([][2]string, 0, len(keys))
		for _, key := range keys {
			props = append(props, [2]string{key, byKey[key]})
		}
		return kind, nil, props, nil
	}
	s, perr := primitiveToString(value)
	if perr != nil {
		return 0, nil, nil, perr
	}
	return kindPrimitive, []string{s}, nil, nil
}

func validateOrderedProps(pairs [][2]string) error {
	seen := make(map[string]struct{}, len(pairs))
	for _, kv := range pairs {
		if _, dup := seen[kv[0]]; dup {
			return fmt.Errorf("object property %q is duplicated", kv[0])
		}
		seen[kv[0]] = struct{}{}
	}
	return nil
}

// primitiveToString returns the canonical string representation of a primitive
// value as defined by the OpenAPI styles: booleans and numbers use the JSON
// representation and strings are returned unchanged.
func primitiveToString(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case json.Number:
		return v.String(), nil
	case float32, float64,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, uintptr:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(value); err != nil {
			return "", err
		}
		return strings.TrimSuffix(buf.String(), "\n"), nil
	case nil:
		return "", fmt.Errorf("a null value cannot be encoded")
	default:
		rv := reflect.ValueOf(value)
		switch rv.Kind() {
		case reflect.Bool:
			return strconv.FormatBool(rv.Bool()), nil
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return strconv.FormatInt(rv.Int(), 10), nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return strconv.FormatUint(rv.Uint(), 10), nil
		case reflect.Float32:
			return strconv.FormatFloat(rv.Float(), 'g', -1, 32), nil
		case reflect.Float64:
			return strconv.FormatFloat(rv.Float(), 'g', -1, 64), nil
		case reflect.String:
			return rv.String(), nil
		}
		return "", fmt.Errorf("a value of type %T cannot be encoded as a parameter: only primitives, arrays of primitives, and objects of primitives are supported", value)
	}
}

func parameterEncodingError(param *openapi3.Parameter, cause error) error {
	return &ParseError{
		Kind:   KindInvalidFormat,
		Reason: fmt.Sprintf("cannot encode parameter %q in %s: %s", param.Name, param.In, cause.Error()),
	}
}

// pathEscape percent-escapes a value of a path parameter per RFC 3986.
func pathEscape(s string) string { return url.PathEscape(s) }

// queryEscape percent-escapes a query key or value. Spaces are encoded as
// %20 (not "+") so the encoding stays a subset of RFC 3986 and round-trips
// through both application/x-www-form-urlencoded and generic query parsers.
func queryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
