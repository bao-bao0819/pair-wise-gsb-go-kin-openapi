package openapi3filter

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// encodeQuery serializes query parameters in styles form, spaceDelimited,
// pipeDelimited and deepObject. Keys are returned percent-escaped; values are
// returned unescaped and escaped when written to a URL.
func encodeQuery(param *openapi3.Parameter, sm *openapi3.SerializationMethod, value *normalizedValue) ([]QueryPair, error) {
	escapedName := queryEscapeKey(param.Name)
	switch value.kind {
	case kindPrimitive:
		if sm.Style != openapi3.SerializationForm {
			return nil, parameterEncodingError(param, KindUnsupportedFormat,
				fmt.Sprintf("style %q applies to arrays or objects only", sm.Style))
		}
		return []QueryPair{{QueryKey: escapedName, QueryValue: value.str}}, nil

	case kindArray:
		return encodeQueryArray(param, sm, escapedName, value.array)

	case kindObject:
		return encodeQueryObject(param, sm, escapedName, value)
	}
	return nil, parameterEncodingError(param, KindUnsupportedFormat,
		fmt.Sprintf("unsupported value kind for location %q", param.In))
}

func encodeQueryArray(param *openapi3.Parameter, sm *openapi3.SerializationMethod, escapedName string, items []string) ([]QueryPair, error) {
	if sm.Style == openapi3.SerializationDeepObject {
		return nil, parameterEncodingError(param, KindUnsupportedFormat,
			"style \"deepObject\" applies to objects only")
	}
	if len(items) == 0 {
		// Deterministic representation of an empty array: a single occurrence
		// with an empty value (form explode would otherwise emit nothing,
		// which cannot be distinguished from an omitted parameter).
		return []QueryPair{{QueryKey: escapedName, QueryValue: ""}}, nil
	}
	if sm.Explode {
		// form, spaceDelimited and pipeDelimited explode to repeated keys.
		pairs := make([]QueryPair, len(items))
		for i, item := range items {
			pairs[i] = QueryPair{QueryKey: escapedName, QueryValue: item}
		}
		return pairs, nil
	}

	var delimiter string
	switch sm.Style {
	case openapi3.SerializationForm:
		delimiter = ","
	case openapi3.SerializationSpaceDelimited:
		delimiter = " "
	case openapi3.SerializationPipeDelimited:
		delimiter = "|"
	default:
		return nil, parameterEncodingError(param, KindUnsupportedFormat,
			fmt.Sprintf("style %q does not apply to arrays", sm.Style))
	}
	if err := rejectAny(param, items, delimiter); err != nil {
		return nil, err
	}
	return []QueryPair{{
		QueryKey:   escapedName,
		QueryValue: strings.Join(items, delimiter),
	}}, nil
}

func encodeQueryObject(param *openapi3.Parameter, sm *openapi3.SerializationMethod, escapedName string, value *normalizedValue) ([]QueryPair, error) {
	for _, key := range value.keys {
		if key == "" {
			return nil, parameterEncodingError(param, KindInvalidFormat, "object has an empty property name")
		}
	}
	switch sm.Style {
	case openapi3.SerializationForm:
		if sm.Explode {
			if len(value.keys) == 0 {
				return nil, parameterEncodingError(param, KindInvalidFormat,
					"an empty object cannot be encoded with style=form, explode=true without pretending a parameter is present")
			}
			pairs := make([]QueryPair, 0, len(value.keys))
			for _, key := range value.keys {
				pairs = append(pairs, QueryPair{
					QueryKey:   queryEscapeKey(key),
					QueryValue: value.values[key],
				})
			}
			return pairs, nil
		}
		if len(value.keys) == 0 {
			return []QueryPair{{QueryKey: escapedName, QueryValue: ""}}, nil
		}
		if err := rejectObjectFlat(param, value, ","); err != nil {
			return nil, err
		}
		flat := make([]string, 0, len(value.keys)*2)
		for _, key := range value.keys {
			flat = append(flat, key, value.values[key])
		}
		return []QueryPair{{QueryKey: escapedName, QueryValue: strings.Join(flat, ",")}}, nil

	case openapi3.SerializationDeepObject:
		if !sm.Explode {
			return nil, parameterEncodingError(param, KindUnsupportedFormat,
				"deepObject requires explode=true")
		}
		if len(value.keys) == 0 {
			return nil, parameterEncodingError(param, KindInvalidFormat,
				"an empty object cannot be encoded with style=deepObject")
		}
		pairs := make([]QueryPair, 0, len(value.keys))
		for _, key := range value.keys {
			// The spec defines deepObject for flat objects only; brackets in a
			// property name would describe nesting and are rejected.
			if strings.ContainsAny(key, "[]") {
				return nil, parameterEncodingError(param, KindInvalidFormat,
					fmt.Sprintf("deepObject property name %q contains brackets: nested objects are not supported", key))
			}
			pairs = append(pairs, QueryPair{
				QueryKey:   escapedName + "[" + queryEscapeKey(key) + "]",
				QueryValue: value.values[key],
			})
		}
		return pairs, nil

	default:
		return nil, parameterEncodingError(param, KindUnsupportedFormat,
			fmt.Sprintf("style %q does not apply to objects", sm.Style))
	}
}

// encodeHeader serializes header parameters, which always use the simple style.
func encodeHeader(param *openapi3.Parameter, sm *openapi3.SerializationMethod, value *normalizedValue) (string, error) {
	switch value.kind {
	case kindPrimitive:
		out, perr := safeHeaderValue(param, value.str)
		if perr != nil {
			return "", perr
		}
		return out, nil

	case kindArray:
		if len(value.array) == 0 {
			return "", nil
		}
		// Header arrays are serialized with comma separators regardless of
		// explode (the simple style has no exploded array form), so a comma
		// inside an item cannot be represented unambiguously.
		if err := rejectAny(param, value.array, ","); err != nil {
			return "", err
		}
		for _, item := range value.array {
			if item != strings.TrimSpace(item) {
				return "", parameterEncodingError(param, KindInvalidFormat,
					fmt.Sprintf("array item %q has leading or trailing whitespace, which header parsing would remove", item))
			}
			if _, perr := safeHeaderValue(param, item); perr != nil {
				return "", perr
			}
		}
		return strings.Join(value.array, ","), nil

	case kindObject:
		if len(value.keys) == 0 {
			return "", nil
		}
		if !sm.Explode {
			if err := rejectObjectFlat(param, value, ","); err != nil {
				return "", err
			}
			flat := make([]string, 0, len(value.keys)*2)
			for _, key := range value.keys {
				flat = append(flat, key, value.values[key])
			}
			out, perr := safeHeaderValue(param, strings.Join(flat, ","))
			if perr != nil {
				return "", perr
			}
			return out, nil
		}
		if err := rejectObjectPairs(param, value, ","); err != nil {
			return "", err
		}
		pairs := make([]string, 0, len(value.keys))
		for _, key := range value.keys {
			pairs = append(pairs, key+"="+value.values[key])
		}
		out, perr := safeHeaderValue(param, strings.Join(pairs, ","))
		if perr != nil {
			return "", perr
		}
		return out, nil
	}
	return "", parameterEncodingError(param, KindUnsupportedFormat, "unsupported value kind for a header parameter")
}

// encodeCookie serializes cookie parameters using the form style without
// explode (a single cookie carries one value).
func encodeCookie(param *openapi3.Parameter, sm *openapi3.SerializationMethod, value *normalizedValue) (string, error) {
	switch value.kind {
	case kindPrimitive:
		out, perr := safeCookieValue(param, value.str)
		if perr != nil {
			return "", perr
		}
		return out, nil

	case kindArray:
		if len(value.array) == 0 {
			return "", nil
		}
		if err := rejectAny(param, value.array, ","); err != nil {
			return "", err
		}
		for _, item := range value.array {
			if _, perr := safeCookieValue(param, item); perr != nil {
				return "", perr
			}
		}
		return strings.Join(value.array, ","), nil

	case kindObject:
		if len(value.keys) == 0 {
			return "", nil
		}
		if err := rejectObjectFlat(param, value, ","); err != nil {
			return "", err
		}
		flat := make([]string, 0, len(value.keys)*2)
		for _, key := range value.keys {
			k, err := safeCookieValue(param, key)
			if err != nil {
				return "", err
			}
			v, err := safeCookieValue(param, value.values[key])
			if err != nil {
				return "", err
			}
			flat = append(flat, k, v)
		}
		return strings.Join(flat, ","), nil
	}
	return "", parameterEncodingError(param, KindUnsupportedFormat, "unsupported value kind for a cookie parameter")
}
