package openapi3filter

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
)

func encodeBody(body any, mediaType string) ([]byte, error) {
	if encoder := RegisteredBodyEncoder(mediaType); encoder != nil {
		return encoder(body)
	}
	return nil, &ParseError{
		Kind:   KindUnsupportedFormat,
		Reason: fmt.Sprintf("%s %q", prefixUnsupportedCT, mediaType),
	}
}

// BodyEncoder really is an (encoding/json).Marshaler
type BodyEncoder func(body any) ([]byte, error)

var bodyEncodersM sync.RWMutex
var bodyEncoders = map[string]BodyEncoder{
	"application/json": json.Marshal,
}

// RegisterBodyEncoder enables package-wide decoding of contentType values
func RegisterBodyEncoder(contentType string, encoder BodyEncoder) {
	if contentType == "" {
		panic("contentType is empty")
	}
	if encoder == nil {
		panic("encoder is not defined")
	}
	bodyEncodersM.Lock()
	bodyEncoders[contentType] = encoder
	bodyEncodersM.Unlock()
}

// UnregisterBodyEncoder disables package-wide decoding of contentType values
func UnregisterBodyEncoder(contentType string) {
	if contentType == "" {
		panic("contentType is empty")
	}
	bodyEncodersM.Lock()
	delete(bodyEncoders, contentType)
	bodyEncodersM.Unlock()
}

// RegisteredBodyEncoder returns the registered body encoder for the given content type.
//
// If no encoder was registered for the given content type, nil is returned.
func RegisteredBodyEncoder(contentType string) BodyEncoder {
	bodyEncodersM.RLock()
	mayBE := bodyEncoders[contentType]
	bodyEncodersM.RUnlock()
	return mayBE
}

// EncodedParameter is the wire encoding of a typed parameter value produced by
// EncodeParameter according to the parameter's location, style and explode.
//
// The fields are populated by location:
//   - Path: PathSegment is set.
//   - Query: QueryPairs holds one or more key/value pairs; values are not
//     percent-escaped, QueryKey is already percent-escaped, and each pair is
//     meant to be joined as "<escaped key>=<escaped value>".
//   - Header: HeaderValue is set (a raw field value without any escaping).
//   - Cookie: HeaderValue holds the raw cookie value (SetOnRequest performs
//     the cookie-specific wire handling via net/http).
type EncodedParameter struct {
	// Parameter is the parameter definition the value was encoded for.
	Parameter *openapi3.Parameter

	// PathSegment is the value to substitute into a path template, including
	// the style prefix (".", ";name=") but excluding the braces.
	PathSegment string

	// QueryPairs holds the generated query occurrences, in emission order.
	// QueryKey is percent-escaped; QueryValue is the unescaped value.
	QueryPairs []QueryPair

	// HeaderValue is the raw value for an HTTP header field or a cookie.
	HeaderValue string
}

// QueryPair is one occurrence of a query parameter: a percent-escaped key and
// an unescaped value that must be percent-escaped when written to a URL.
type QueryPair struct {
	QueryKey   string
	QueryValue string
}

// EncodeParameter serializes value for param according to its "in", "style",
// "explode" and "allowReserved" properties.
//
// value must be a primitive (string or a Go bool/[u]int*/float*/json.Number),
// a slice/array of primitives, or a map with string keys and primitive values.
// Structured values that cannot be represented unambiguously by the chosen
// serialization method (nested objects or arrays, structural delimiters in a
// value, ...) produce a *ParseError with a parameter-specific reason, so no
// information is silently lost.
//
// EncodeParameter never mutates a request: it only computes the encoding. Use
// EncodedParameter.SetOnRequest (or EncodeRequestParameter) to write the result.
func EncodeParameter(param *openapi3.Parameter, value any) (*EncodedParameter, error) {
	if param == nil {
		return nil, &ParseError{Kind: KindOther, Reason: "parameter is nil"}
	}
	if param.Content != nil {
		return nil, &ParseError{
			Kind:   KindUnsupportedFormat,
			Value:  param.Name,
			Reason: fmt.Sprintf("parameter %q is defined with content, which is not supported by the parameter encoder", param.Name),
		}
	}
	sm, err := param.SerializationMethod()
	if err != nil {
		return nil, &ParseError{Kind: KindOther, Value: param.Name, Cause: err}
	}
	if param.AllowReserved && param.In != openapi3.ParameterInQuery {
		return nil, parameterEncodingError(param, KindInvalidFormat,
			"allowReserved is allowed for query parameters only")
	}
	if err := validateSerializationMethod(param, sm); err != nil {
		return nil, err
	}

	normalized, normErr := normalizeValue(value)
	if normErr != nil {
		return nil, parameterEncodingError(param, normErr.Kind, normErr.Reason)
	}

	out := &EncodedParameter{Parameter: param}
	switch param.In {
	case openapi3.ParameterInPath:
		out.PathSegment, err = encodePath(param, sm, normalized)
	case openapi3.ParameterInQuery:
		out.QueryPairs, err = encodeQuery(param, sm, normalized)
	case openapi3.ParameterInHeader:
		out.HeaderValue, err = encodeHeader(param, sm, normalized)
	case openapi3.ParameterInCookie:
		out.HeaderValue, err = encodeCookie(param, sm, normalized)
	default:
		err = &ParseError{Kind: KindOther, Value: param.In,
			Reason: fmt.Sprintf("parameter %q has unsupported location %q", param.Name, param.In)}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EncodeRequestParameter serializes value for param and writes the encoding to
// request. It is a convenience wrapper around EncodeParameter and
// EncodedParameter.SetOnRequest: the request is left untouched on error, so an
// unsupported style/explode combination is reported before any URL or header
// is modified.
func EncodeRequestParameter(request *http.Request, param *openapi3.Parameter, value any) error {
	encoded, err := EncodeParameter(param, value)
	if err != nil {
		return err
	}
	return encoded.SetOnRequest(request)
}

// SetOnRequest writes the encoding to request:
//   - path parameters replace the "{name}" placeholder of request.URL.Path;
//   - query parameters are appended to request.URL.RawQuery, preserving the
//     existing entries, their order and any unrelated duplicates;
//   - header parameters set the named field of request.Header;
//   - cookie parameters are added through request.AddCookie.
//
// A path placeholder is required for path parameters.
func (encoded *EncodedParameter) SetOnRequest(request *http.Request) error {
	if request == nil || request.URL == nil {
		return &ParseError{Kind: KindOther, Reason: "request or request URL is nil"}
	}
	param := encoded.Parameter
	switch param.In {
	case openapi3.ParameterInPath:
		placeholder := "{" + param.Name + "}"
		rawEscaped := request.URL.EscapedPath()
		encodedPlaceholder := url.PathEscape(placeholder)
		newRawPath, rawCount := replaceToken(rawEscaped, encodedPlaceholder, encoded.PathSegment)
		if rawCount == 0 {
			newRawPath, rawCount = replaceToken(rawEscaped, placeholder, encoded.PathSegment)
		}
		// PathSegment is already escaped, so its decoded form is what Path
		// must hold; decode it for the semantic Path (best effort).
		decodedSegment, derr := url.PathUnescape(encoded.PathSegment)
		if derr != nil {
			return parameterEncodingError(param, KindInvalidFormat,
				fmt.Sprintf("path segment %q is not a valid percent-encoding: %v", encoded.PathSegment, derr))
		}
		newPath, pathCount := replaceToken(request.URL.Path, placeholder, decodedSegment)
		if rawCount == 0 || pathCount == 0 {
			return parameterEncodingError(param, KindInvalidFormat,
				fmt.Sprintf("path placeholder %q is not present in %q", placeholder, request.URL.Path))
		}
		request.URL.Path = newPath
		if request.URL.RawPath != "" || newRawPath != newPath {
			request.URL.RawPath = newRawPath
		} else {
			request.URL.RawPath = ""
		}
	case openapi3.ParameterInQuery:
		var existing string
		if request.URL.RawQuery != "" {
			existing = request.URL.RawQuery
		}
		var parts []string
		if existing != "" {
			parts = append(parts, existing)
		}
		for _, pair := range encoded.QueryPairs {
			parts = append(parts, pair.QueryKey+"="+escapeQueryValue(pair.QueryValue, param.AllowReserved))
		}
		if len(parts) > 0 {
			request.URL.RawQuery = strings.Join(parts, "&")
			request.URL.ForceQuery = true
		}
	case openapi3.ParameterInHeader:
		if request.Header == nil {
			request.Header = http.Header{}
		}
		request.Header[http.CanonicalHeaderKey(param.Name)] = []string{encoded.HeaderValue}
	case openapi3.ParameterInCookie:
		request.AddCookie(&http.Cookie{Name: param.Name, Value: encoded.HeaderValue})
	default:
		return parameterEncodingError(param, KindOther, fmt.Sprintf("unsupported parameter location %q", param.In))
	}
	return nil
}

// replaceToken replaces every occurrence of old with new, returning the result
// and the number of replacements.
func replaceToken(s, old, new string) (string, int) {
	if old == "" {
		return s, 0
	}
	count := 0
	var b strings.Builder
	for {
		idx := strings.Index(s, old)
		if idx < 0 {
			b.WriteString(s)
			return b.String(), count
		}
		count++
		b.WriteString(s[:idx])
		b.WriteString(new)
		s = s[idx+len(old):]
	}
}

// validateSerializationMethod reports style/explode combinations that are not
// defined by the OpenAPI 3 specification for the parameter's location.
func validateSerializationMethod(param *openapi3.Parameter, sm *openapi3.SerializationMethod) error {
	supported := false
	switch {
	case param.In == openapi3.ParameterInPath:
		switch sm.Style {
		case openapi3.SerializationSimple, openapi3.SerializationLabel, openapi3.SerializationMatrix:
			supported = true
		}
	case param.In == openapi3.ParameterInQuery:
		switch sm.Style {
		case openapi3.SerializationForm:
			supported = true
		case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
			supported = true
		case openapi3.SerializationDeepObject:
			// deepObject is defined for objects with explode=true only.
			supported = sm.Explode
		}
	case param.In == openapi3.ParameterInHeader:
		supported = sm.Style == openapi3.SerializationSimple
	case param.In == openapi3.ParameterInCookie:
		// RFC 6265 offers a single value; the exploded forms of arrays and
		// objects are not representable.
		supported = sm.Style == openapi3.SerializationForm && !sm.Explode
	}
	if !supported {
		return parameterEncodingError(param, KindUnsupportedFormat,
			fmt.Sprintf("unsupported serialization method: style=%q, explode=%v for location %q",
				sm.Style, sm.Explode, param.In))
	}
	return nil
}

func parameterEncodingError(param *openapi3.Parameter, kind ParseErrorKind, reason string) *ParseError {
	return &ParseError{
		Kind:   kind,
		Value:  param.Name,
		Reason: fmt.Sprintf("parameter %q in %s: %s", param.Name, param.In, reason),
	}
}

// normalized kinds.
const (
	kindPrimitive = iota
	kindArray
	kindObject
)

type normalizedValue struct {
	kind  int
	str   string
	array []string
	// keys/objects are kept in insertion-independent, sorted order so the
	// encoding is deterministic for maps.
	keys   []string
	values map[string]string
}

// normalizeValue converts value into primitives, []string or a sorted
// map[string]string. Nested structured values are rejected.
func normalizeValue(value any) (*normalizedValue, *ParseError) {
	return normalizeReflect(reflect.ValueOf(value))
}

func normalizeReflect(rv reflect.Value) (*normalizedValue, *ParseError) {
	for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
		if rv.IsNil() {
			return nil, &ParseError{Kind: KindInvalidFormat, Reason: "value is nil"}
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return nil, &ParseError{Kind: KindInvalidFormat, Reason: "value is nil"}
	}

	switch rv.Kind() {
	case reflect.String:
		return &normalizedValue{kind: kindPrimitive, str: rv.String()}, nil
	case reflect.Bool:
		return &normalizedValue{kind: kindPrimitive, str: strconv.FormatBool(rv.Bool())}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &normalizedValue{kind: kindPrimitive, str: strconv.FormatInt(rv.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return &normalizedValue{kind: kindPrimitive, str: strconv.FormatUint(rv.Uint(), 10)}, nil
	case reflect.Float32:
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, &ParseError{Kind: KindInvalidFormat, Value: f, Reason: "non-finite number cannot be encoded"}
		}
		return &normalizedValue{kind: kindPrimitive, str: strconv.FormatFloat(f, 'g', -1, 32)}, nil
	case reflect.Float64:
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, &ParseError{Kind: KindInvalidFormat, Value: f, Reason: "non-finite number cannot be encoded"}
		}
		return &normalizedValue{kind: kindPrimitive, str: strconv.FormatFloat(f, 'g', -1, 64)}, nil
	case reflect.Slice, reflect.Array:
		items := make([]string, rv.Len())
		for i := range rv.Len() {
			item, perr := normalizeReflect(rv.Index(i))
			if perr != nil {
				return nil, &ParseError{path: []any{i}, Kind: perr.Kind, Reason: "array item: " + perr.Reason, Cause: perr}
			}
			if item.kind != kindPrimitive {
				return nil, &ParseError{path: []any{i}, Kind: KindInvalidFormat,
					Reason: "nested arrays and objects are not supported by the parameter styles"}
			}
			items[i] = item.str
		}
		return &normalizedValue{kind: kindArray, array: items}, nil
	case reflect.Map:
		rt := rv.Type()
		if rt.Key().Kind() != reflect.String {
			return nil, &ParseError{Kind: KindInvalidFormat,
				Reason: "object keys must be strings, got " + rt.Key().Kind().String()}
		}
		keys := make([]string, 0, rv.Len())
		values := make(map[string]string, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			item, perr := normalizeReflect(iter.Value())
			if perr != nil {
				return nil, &ParseError{path: []any{key}, Kind: perr.Kind, Reason: "object property: " + perr.Reason, Cause: perr}
			}
			if item.kind != kindPrimitive {
				return nil, &ParseError{path: []any{key}, Kind: KindInvalidFormat,
					Reason: "nested arrays and objects are not supported by the parameter styles"}
			}
			keys = append(keys, key)
			values[key] = item.str
		}
		sort.Strings(keys)
		return &normalizedValue{kind: kindObject, keys: keys, values: values}, nil
	default:
		if n, ok := rv.Interface().(json.Number); ok {
			return &normalizedValue{kind: kindPrimitive, str: n.String()}, nil
		}
		return nil, &ParseError{Kind: KindInvalidFormat, Value: rv.Interface(),
			Reason: fmt.Sprintf("unsupported value type %s", rv.Type())}
	}
}
