package openapi3filter

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// encodePath serializes path parameters in styles simple, label and matrix.
func encodePath(param *openapi3.Parameter, sm *openapi3.SerializationMethod, value *normalizedValue) (string, error) {
	name := param.Name
	switch value.kind {
	case kindPrimitive:
		escaped := escapePathValue(value.str)
		return withPathStyle(sm, name, escaped), nil

	case kindArray:
		items := value.array
		if len(items) == 0 {
			return encodeEmptyPath(param, sm), nil
		}
		if err := rejectPathArrayAmbiguities(param, sm, items); err != nil {
			return "", err
		}
		return encodePathArray(param, sm, escapeAll(items)), nil

	case kindObject:
		if len(value.keys) == 0 {
			return encodeEmptyPath(param, sm), nil
		}
		if err := rejectPathObjectAmbiguities(param, sm, value); err != nil {
			return "", err
		}
		return encodePathObject(param, sm, value), nil
	}
	return "", parameterEncodingError(param, KindUnsupportedFormat,
		fmt.Sprintf("unsupported value kind for location %q", param.In))
}

func withPathStyle(sm *openapi3.SerializationMethod, name, encoded string) string {
	switch sm.Style {
	case openapi3.SerializationLabel:
		return "." + encoded
	case openapi3.SerializationMatrix:
		return ";" + name + "=" + encoded
	default:
		return encoded
	}
}

func encodePathArray(param *openapi3.Parameter, sm *openapi3.SerializationMethod, items []string) string {
	switch {
	case sm.Style == openapi3.SerializationSimple:
		return strings.Join(items, ",")
	case sm.Style == openapi3.SerializationLabel && !sm.Explode:
		return "." + strings.Join(items, ",")
	case sm.Style == openapi3.SerializationLabel && sm.Explode:
		return "." + strings.Join(items, ".")
	case sm.Style == openapi3.SerializationMatrix && !sm.Explode:
		return ";" + param.Name + "=" + strings.Join(items, ",")
	case sm.Style == openapi3.SerializationMatrix && sm.Explode:
		delim := ";" + param.Name + "="
		return delim + strings.Join(items, delim)
	}
	return ""
}

func encodePathObject(param *openapi3.Parameter, sm *openapi3.SerializationMethod, value *normalizedValue) string {
	flat := make([]string, 0, len(value.keys)*2)
	pairs := make([]string, 0, len(value.keys))
	for _, key := range value.keys {
		k := escapePathValue(key)
		v := escapePathValue(value.values[key])
		flat = append(flat, k, v)
		pairs = append(pairs, k+"="+v)
	}
	switch {
	case sm.Style == openapi3.SerializationSimple && !sm.Explode:
		return strings.Join(flat, ",")
	case sm.Style == openapi3.SerializationSimple && sm.Explode:
		return strings.Join(pairs, ",")
	case sm.Style == openapi3.SerializationLabel && !sm.Explode:
		return "." + strings.Join(flat, ",")
	case sm.Style == openapi3.SerializationLabel && sm.Explode:
		return "." + strings.Join(pairs, ".")
	case sm.Style == openapi3.SerializationMatrix && !sm.Explode:
		return ";" + param.Name + "=" + strings.Join(flat, ",")
	case sm.Style == openapi3.SerializationMatrix && sm.Explode:
		return ";" + strings.Join(pairs, ";")
	}
	return ""
}

// encodeEmptyPath returns the deterministic encoding of an empty array or
// object for a path style.
func encodeEmptyPath(param *openapi3.Parameter, sm *openapi3.SerializationMethod) string {
	switch sm.Style {
	case openapi3.SerializationLabel:
		return "."
	case openapi3.SerializationMatrix:
		return ";" + param.Name + "="
	default:
		return ""
	}
}

func rejectPathArrayAmbiguities(param *openapi3.Parameter, sm *openapi3.SerializationMethod, items []string) error {
	switch {
	case sm.Style == openapi3.SerializationSimple:
		return rejectAny(param, items, ",")
	case sm.Style == openapi3.SerializationLabel:
		if sm.Explode {
			return rejectAny(param, items, ".")
		}
		return rejectAny(param, items, ",")
	case sm.Style == openapi3.SerializationMatrix:
		if sm.Explode {
			delim := ";" + param.Name + "="
			for _, item := range items {
				if strings.Contains(item, delim) {
					return ambiguous(param, item, delim)
				}
			}
			return nil
		}
		return rejectAny(param, items, ",")
	}
	return nil
}

func rejectPathObjectAmbiguities(param *openapi3.Parameter, sm *openapi3.SerializationMethod, value *normalizedValue) error {
	for _, key := range value.keys {
		if key == "" {
			return parameterEncodingError(param, KindInvalidFormat, "object has an empty property name")
		}
	}
	switch {
	case sm.Style == openapi3.SerializationSimple && !sm.Explode:
		return rejectObjectFlat(param, value, ",")
	case sm.Style == openapi3.SerializationSimple && sm.Explode:
		return rejectObjectPairs(param, value, ",")
	case sm.Style == openapi3.SerializationLabel && !sm.Explode:
		return rejectObjectFlat(param, value, ",")
	case sm.Style == openapi3.SerializationLabel && sm.Explode:
		return rejectObjectPairs(param, value, ".")
	case sm.Style == openapi3.SerializationMatrix && !sm.Explode:
		return rejectObjectFlat(param, value, ",")
	case sm.Style == openapi3.SerializationMatrix && sm.Explode:
		return rejectObjectPairs(param, value, ";")
	}
	return nil
}

// rejectObjectFlat rejects names/values containing delimiter for the
// non-exploded "<key>,<value>,..." serialization where keys and values share
// the same delimiter.
func rejectObjectFlat(param *openapi3.Parameter, value *normalizedValue, delimiter string) error {
	for _, key := range value.keys {
		if strings.Contains(key, delimiter) {
			return ambiguous(param, key, delimiter)
		}
		if strings.Contains(value.values[key], delimiter) {
			return ambiguous(param, value.values[key], delimiter)
		}
	}
	return nil
}

// rejectObjectPairs rejects property delimiters inside names for the exploded
// "<k>=<v>" pair serialization. The project's decoder treats "=" as a strict
// name/value separator (a value containing "=" cannot be parsed back), so "="
// in names and values is rejected as well to keep encoding and validation
// consistent and avoid silent data loss.
func rejectObjectPairs(param *openapi3.Parameter, value *normalizedValue, propDelim string) error {
	for _, key := range value.keys {
		if strings.Contains(key, propDelim) {
			return ambiguous(param, key, propDelim)
		}
		if strings.ContainsAny(key, "=") {
			return ambiguous(param, key, "=")
		}
		if strings.Contains(value.values[key], propDelim) {
			return ambiguous(param, value.values[key], propDelim)
		}
		if strings.Contains(value.values[key], "=") {
			return ambiguous(param, value.values[key], "=")
		}
	}
	return nil
}

func rejectAny(param *openapi3.Parameter, values []string, delimiter string) error {
	for _, v := range values {
		if strings.Contains(v, delimiter) {
			return ambiguous(param, v, delimiter)
		}
	}
	return nil
}

func ambiguous(param *openapi3.Parameter, value any, delimiter string) *ParseError {
	return parameterEncodingError(param, KindInvalidFormat,
		fmt.Sprintf("value %v contains the structural delimiter %q and cannot be encoded unambiguously", value, delimiter))
}
