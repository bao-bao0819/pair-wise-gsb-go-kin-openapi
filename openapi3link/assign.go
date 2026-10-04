package openapi3link

import (
	"reflect"

	"github.com/getkin/kin-openapi/openapi3"
)

// schemaAllowsNull reports whether schema accepts a JSON null. In OpenAPI 3.0
// this is the "nullable" keyword; 3.1 uses the "null" type instead.
func schemaAllowsNull(schema *openapi3.Schema) bool {
	if schema == nil {
		return true
	}
	if schema.Nullable {
		return true
	}
	return schema.Type != nil && schema.Type.IncludesNull()
}

// jsonValueMatchesSchema performs a lightweight structural check that the
// decoded value is inside the schema's assignable range. It verifies type,
// enumeration and basic composition (allOf/anyOf/oneOf) but does not run full
// JSON Schema validation: the encoding layer is expected to validate.
func jsonValueMatchesSchema(value any, schema *openapi3.Schema) bool {
	if schema == nil {
		return true
	}

	if len(schema.Enum) > 0 && !enumContains(schema.Enum, value) {
		return false
	}

	if len(schema.AllOf) > 0 {
		for _, ref := range schema.AllOf {
			if !assignable(value, schemaOf(ref)) {
				return false
			}
		}
	}
	if len(schema.AnyOf) > 0 {
		matched := false
		for _, ref := range schema.AnyOf {
			if assignable(value, schemaOf(ref)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(schema.OneOf) > 0 {
		matches := 0
		for _, ref := range schema.OneOf {
			if assignable(value, schemaOf(ref)) {
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}

	if schema.Type == nil || schema.Type.IsEmpty() {
		return true
	}

	for _, typ := range schema.Type.Slice() {
		if jsonTypeMatches(value, typ, schema) {
			return true
		}
	}
	return false
}

func jsonTypeMatches(value any, typ string, schema *openapi3.Schema) bool {
	if typ == openapi3.TypeNull {
		return value == nil
	}
	switch value.(type) {
	case bool:
		return typ == openapi3.TypeBoolean
	case string:
		// A header or query value captured from an HTTP message is always a
		// string; assigning it to a string parameter is valid. Numeric
		// parameters are the encoding layer's responsibility (e.g. style
		// expansion) and are not coerced here.
		return typ == openapi3.TypeString
	case float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		if typ == openapi3.TypeNumber {
			return true
		}
		if typ == openapi3.TypeInteger {
			return isInteger(value)
		}
		return false
	case map[string]any:
		return typ == openapi3.TypeObject
	case []any:
		if typ != openapi3.TypeArray {
			return false
		}
		return arrayItemsAssignable(value.([]any), schema)
	case []string:
		// Repeated query/header values captured as []string.
		if typ != openapi3.TypeArray {
			return false
		}
		for _, item := range value.([]string) {
			if !assignable(item, itemsSchema(schema)) {
				return false
			}
		}
		return true
	default:
		// Unknown Go types (e.g. json.Number) are not rejected as long as the
		// schema itself is untyped; typed schemas require JSON-decoded input.
		return false
	}
}

func arrayItemsAssignable(items []any, schema *openapi3.Schema) bool {
	itemSchema := itemsSchema(schema)
	for _, item := range items {
		if !assignable(item, itemSchema) {
			return false
		}
	}
	return true
}

func itemsSchema(schema *openapi3.Schema) *openapi3.Schema {
	if schema != nil && schema.Items != nil {
		return schema.Items.Value
	}
	return nil
}

func schemaOf(ref *openapi3.SchemaRef) *openapi3.Schema {
	if ref == nil {
		return nil
	}
	return ref.Value
}

func isInteger(value any) bool {
	switch number := value.(type) {
	case float64:
		return number == float64(int64(number))
	case float32:
		return number == float32(int64(number))
	default:
		// All concrete Go integer kinds are already integers.
		return true
	}
}

func enumContains(enum []any, value any) bool {
	for _, candidate := range enum {
		if valuesEqual(candidate, value) {
			return true
		}
	}
	return false
}

// valuesEqual compares an enum candidate with a value. JSON decoding produces
// float64 for every number, so a plain DeepEqual suffices for decoded input;
// numbers produced by $statusCode (int) still need a numeric comparison.
func valuesEqual(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	return asFloat(a) == asFloat(b) && isNumber(a) && isNumber(b)
}

func isNumber(value any) bool {
	switch value.(type) {
	case float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func asFloat(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case float32:
		return float64(number)
	case int:
		return float64(number)
	case int8:
		return float64(number)
	case int16:
		return float64(number)
	case int32:
		return float64(number)
	case int64:
		return float64(number)
	case uint:
		return float64(number)
	case uint8:
		return float64(number)
	case uint16:
		return float64(number)
	case uint32:
		return float64(number)
	case uint64:
		return float64(number)
	default:
		return 0
	}
}
