package openapi3filter

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// queryComponentEscape percent-escapes a query key or value. Keys are always
// escaped fully. Values honor allowReserved: the RFC 3986 reserved set allowed
// by OpenAPI's allowReserved is left raw, while the query structure characters
// and the style's own item delimiters are always escaped so a reserved
// character inside a value can never be parsed as a separator.
func queryComponentEscape(param *openapi3.Parameter, s string, isKey bool) string {
	if isKey || !param.AllowReserved {
		return queryEscape(s)
	}
	return escapeQueryAllowReserved(s)
}

// allowedByAllowReserved lists the RFC 3986 reserved characters that
// OpenAPI 3.0.3 section 4.8.12.3 allows to stay unescaped in a query value:
// :/?#[]@!$&'()*+,;=. The query separator "&" is excluded because leaving it
// raw splits the value into another parameter.
const allowedByAllowReserved = ":/?#[]@!$'()*+,;="

func escapeQueryAllowReserved(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '&' || !isQueryAllowedReserved(c) {
			b.WriteString(queryEscape(string(c)))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func isQueryAllowedReserved(c byte) bool {
	return strings.IndexByte(allowedByAllowReserved, c) >= 0
}

// encodeQuery serializes a query parameter with style form, spaceDelimited,
// pipeDelimited or deepObject. Keys and values are escaped independently.
func encodeQuery(out *EncodedParameter, param *openapi3.Parameter, sm *openapi3.SerializationMethod,
	kind valueKind, values []string, props [][2]string,
) error {
	name := queryComponentEscape(param, param.Name, true)

	switch sm.Style {
	case openapi3.SerializationForm:
		switch kind {
		case kindPrimitive:
			out.Query = []string{name + "=" + queryComponentEscape(param, values[0], false)}
		case kindArray:
			if len(values) == 0 {
				return errEmptyComposite(sm.Style)
			}
			if sm.Explode {
				out.Query = make([]string, len(values))
				for i, v := range values {
					out.Query[i] = name + "=" + queryComponentEscape(param, v, false)
				}
			} else {
				if err := rejectValueDelimiters(values, ",", sm.Style); err != nil {
					return err
				}
				out.Query = []string{name + "=" + strings.Join(escapeQueryValues(param, values), ",")}
			}
		case kindObject:
			if len(props) == 0 {
				return errEmptyComposite(sm.Style)
			}
			if sm.Explode {
				out.Query = make([]string, len(props))
				for i, kv := range props {
					out.Query[i] = queryComponentEscape(param, kv[0], true) + "=" +
						queryComponentEscape(param, kv[1], false)
				}
			} else {
				if err := rejectObjectDelimiters(props, ",", sm.Style); err != nil {
					return err
				}
				parts := make([]string, 0, len(props)*2)
				for _, kv := range props {
					parts = append(parts,
						queryComponentEscape(param, kv[0], true),
						queryComponentEscape(param, kv[1], false))
				}
				out.Query = []string{name + "=" + strings.Join(parts, ",")}
			}
		}
	case openapi3.SerializationSpaceDelimited, openapi3.SerializationPipeDelimited:
		// OpenAPI defines spaceDelimited/pipeDelimited for arrays only; explode
		// arrays use repeated keys exactly like form explode.
		delim := "|"
		if sm.Style == openapi3.SerializationSpaceDelimited {
			delim = " "
		}
		switch kind {
		case kindPrimitive:
			return unsupportedSerializationMethodError(param, sm)
		case kindArray:
			if len(values) == 0 {
				return errEmptyComposite(sm.Style)
			}
			if sm.Explode {
				out.Query = make([]string, len(values))
				for i, v := range values {
					out.Query[i] = name + "=" + queryComponentEscape(param, v, false)
				}
			} else {
				if err := rejectValueDelimiters(values, delim, sm.Style); err != nil {
					return err
				}
				wireDelim := queryEscape(delim)
				if delim == "|" {
					wireDelim = "|" // "|" is an RFC 3986 sub-delim allowed raw in query
				}
				out.Query = []string{name + "=" + strings.Join(escapeQueryValues(param, values), wireDelim)}
			}
		case kindObject:
			return unsupportedSerializationMethodError(param, sm)
		}
	case openapi3.SerializationDeepObject:
		// deepObject is defined only for objects with explode=true. Only the
		// flat object described by the OpenAPI examples is promised: nested
		// objects/arrays have no normative representation.
		if !sm.Explode || kind != kindObject {
			return unsupportedSerializationMethodError(param, sm)
		}
		if len(props) == 0 {
			return errEmptyComposite(sm.Style)
		}
		out.Query = make([]string, len(props))
		for i, kv := range props {
			if strings.ContainsAny(kv[0], "[]") {
				return fmt.Errorf("deepObject property name %q contains brackets that would be parsed as nesting", kv[0])
			}
			out.Query[i] = name + "[" + queryEscape(kv[0]) + "]=" + queryComponentEscape(param, kv[1], false)
		}
	default:
		return unsupportedSerializationMethodError(param, sm)
	}
	return nil
}

func escapeQueryValues(param *openapi3.Parameter, values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = queryComponentEscape(param, v, false)
	}
	return out
}

// rejectValueDelimiters rejects raw item delimiters even when allowReserved
// is set, because allowReserved only permits reserved characters through the
// query encoding, not through the style's own structure.
func rejectValueDelimiters(values []string, delims, style string) error {
	for i, v := range values {
		if strings.ContainsAny(v, delims) {
			return fmt.Errorf("array item %d contains %q, the item delimiter of style %q", i, delims, style)
		}
	}
	return nil
}

func rejectObjectDelimiters(props [][2]string, delims, style string) error {
	for _, kv := range props {
		if strings.ContainsAny(kv[0], delims) || strings.ContainsAny(kv[1], delims) {
			return fmt.Errorf("object property %q contains %q, the item delimiter of style %q", kv[0], delims, style)
		}
	}
	return nil
}
