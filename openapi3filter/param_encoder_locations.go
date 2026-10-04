package openapi3filter

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// errEmptyComposite is the shared reason for empty arrays/objects: none of
// the OpenAPI parameter styles can serialize them so that a decoder
// distinguishes them from a missing parameter or an empty primitive.
func errEmptyComposite(style string) error {
	return fmt.Errorf("an empty array/object cannot be represented with style %q", style)
}

// forbidAny returns an error naming the first of forbidden characters found
// in s.
func forbidAny(what, s, forbidden string) error {
	if i := strings.IndexAny(s, forbidden); i >= 0 {
		return fmt.Errorf("%s contains %q, a structural delimiter that cannot be distinguished from the style's separator", what, s[i])
	}
	return nil
}

// encodePath serializes a path parameter (style simple, label or matrix).
// Values are RFC 3986 percent-escaped, but a router that matches the decoded
// path cannot tell a percent-escaped separator inside a value from the
// style's own separators, so raw structural characters in values are rejected
// instead of producing a request that decodes to a different value.
func encodePath(out *EncodedParameter, param *openapi3.Parameter, sm *openapi3.SerializationMethod,
	kind valueKind, values []string, props [][2]string,
) error {
	switch sm.Style {
	case openapi3.SerializationSimple:
		switch kind {
		case kindPrimitive:
			out.PathSegment = pathEscape(values[0])
		case kindArray:
			if len(values) == 0 {
				return errEmptyComposite(sm.Style)
			}
			for i, v := range values {
				if err := forbidAny(fmt.Sprintf("array item %d", i), v, ","); err != nil {
					return err
				}
			}
			out.PathSegment = strings.Join(pathEscapeAll(values), ",")
		case kindObject:
			if len(props) == 0 {
				return errEmptyComposite(sm.Style)
			}
			valueSep := ","
			if sm.Explode {
				valueSep = "="
			}
			for _, kv := range props {
				if err := forbidAny("object property name", kv[0], ",="); err != nil {
					return err
				}
				if err := forbidAny(fmt.Sprintf("object property %q value", kv[0]), kv[1], ","+valueSep); err != nil {
					return err
				}
			}
			out.PathSegment = joinObject(pathEscapePairs(props), sm.Explode, ",")
		}
	case openapi3.SerializationLabel:
		switch kind {
		case kindPrimitive:
			out.PathSegment = "." + pathEscape(values[0])
		case kindArray:
			if len(values) == 0 {
				return errEmptyComposite(sm.Style)
			}
			delim := ","
			if sm.Explode {
				delim = "."
			}
			for i, v := range values {
				if err := forbidAny(fmt.Sprintf("array item %d", i), v, delim); err != nil {
					return err
				}
			}
			out.PathSegment = "." + strings.Join(pathEscapeAll(values), delim)
		case kindObject:
			if len(props) == 0 {
				return errEmptyComposite(sm.Style)
			}
			propDelim := ","
			if sm.Explode {
				propDelim = "."
			}
			valueSep := ","
			if sm.Explode {
				valueSep = "="
			}
			for _, kv := range props {
				if err := forbidAny("object property name", kv[0], propDelim+valueSep); err != nil {
					return err
				}
				if err := forbidAny(fmt.Sprintf("object property %q value", kv[0]), kv[1], propDelim+valueSep); err != nil {
					return err
				}
			}
			out.PathSegment = "." + joinObject(pathEscapePairs(props), sm.Explode, propDelim)
		}
	case openapi3.SerializationMatrix:
		switch kind {
		case kindPrimitive:
			out.PathSegment = ";" + param.Name + "=" + pathEscape(values[0])
		case kindArray:
			if len(values) == 0 {
				return errEmptyComposite(sm.Style)
			}
			if sm.Explode {
				for i, v := range values {
					if err := forbidAny(fmt.Sprintf("array item %d", i), v, ";"); err != nil {
						return err
					}
				}
				parts := make([]string, len(values))
				for i, v := range pathEscapeAll(values) {
					parts[i] = ";" + param.Name + "=" + v
				}
				out.PathSegment = strings.Join(parts, "")
			} else {
				for i, v := range values {
					if err := forbidAny(fmt.Sprintf("array item %d", i), v, ","); err != nil {
						return err
					}
				}
				out.PathSegment = ";" + param.Name + "=" + strings.Join(pathEscapeAll(values), ",")
			}
		case kindObject:
			if len(props) == 0 {
				return errEmptyComposite(sm.Style)
			}
			escaped := pathEscapePairs(props)
			if sm.Explode {
				for _, kv := range props {
					if err := forbidAny("object property name", kv[0], ";="); err != nil {
						return err
					}
					if err := forbidAny(fmt.Sprintf("object property %q value", kv[0]), kv[1], ";="); err != nil {
						return err
					}
				}
				parts := make([]string, 0, len(escaped))
				for _, kv := range escaped {
					parts = append(parts, ";"+kv[0]+"="+kv[1])
				}
				out.PathSegment = strings.Join(parts, "")
			} else {
				for _, kv := range props {
					if err := forbidAny("object property name", kv[0], ","); err != nil {
						return err
					}
					if err := forbidAny(fmt.Sprintf("object property %q value", kv[0]), kv[1], ","); err != nil {
						return err
					}
				}
				out.PathSegment = ";" + param.Name + "=" + joinObject(escaped, false, ",")
			}
		}
	default:
		return unsupportedSerializationMethodError(param, sm)
	}
	return nil
}

func pathEscapeAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = pathEscape(v)
	}
	return out
}

func pathEscapePairs(props [][2]string) [][2]string {
	out := make([][2]string, len(props))
	for i, kv := range props {
		out[i] = [2]string{pathEscape(kv[0]), pathEscape(kv[1])}
	}
	return out
}

// joinObject serializes already-escaped object properties: explode produces
// "k=v" pairs, non-explode produces alternating "k","v" tokens; both are
// joined on propDelim.
func joinObject(props [][2]string, explode bool, propDelim string) string {
	parts := make([]string, 0, len(props)*2)
	for _, kv := range props {
		if explode {
			parts = append(parts, kv[0]+"="+kv[1])
		} else {
			parts = append(parts, kv[0], kv[1])
		}
	}
	return strings.Join(parts, propDelim)
}

func unsupportedSerializationMethodError(param *openapi3.Parameter, sm *openapi3.SerializationMethod) error {
	return fmt.Errorf("style %q with explode=%v is not allowed for %s parameters", sm.Style, sm.Explode, param.In)
}

// setEncodedQuery replaces existing occurrences of key in req.URL.RawQuery
// with the provided encoded pairs, preserving the order and exact encoding of
// unrelated pairs. The new pairs are appended at the end of the query string.
func setEncodedQuery(req *http.Request, key string, pairs []string) {
	raw := req.URL.RawQuery
	var head, fragment string
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		head, fragment = raw[:i], raw[i:]
	} else {
		head = raw
	}
	kept := make([]string, 0)
	if head != "" {
		for _, pair := range strings.Split(head, "&") {
			k, _, _ := strings.Cut(pair, "=")
			decoded, err := url.QueryUnescape(k)
			if err != nil {
				decoded = k
			}
			if decoded == key {
				continue
			}
			kept = append(kept, pair)
		}
	}
	kept = append(kept, pairs...)
	req.URL.RawQuery = strings.Join(kept, "&") + fragment
}
