package openapi3plan

import (
	"fmt"
	"math"

	"github.com/getkin/kin-openapi/openapi3"
)

// canonicalCandidate maps a caller-provided concrete value to the Go type the
// ordinary parameter decoder produces on the wire, so a planned candidate and
// the value re-read from its generated request compare equal. Without this,
// callers writing plain int literals would see decoded int32 values.
//
// Integers follow the schema: format "int32" yields int32, otherwise int64
// (matching parsePrimitiveCase). Numbers yield float64. Strings and booleans
// stay as-is.
func canonicalCandidate(param *openapi3.Parameter, v any) (Candidate, error) {
	if v == nil {
		return Null(), nil
	}
	if param.Schema != nil && param.Schema.Value != nil {
		schema := param.Schema.Value
		if schema.Type != nil && schema.Type.IsSingle() {
			switch {
			case schema.Type.Is("integer"):
				n, err := toInt64(v)
				if err != nil {
					return Candidate{}, fmt.Errorf("parameter %q: %w", param.Name, err)
				}
				if schema.Format == "int32" || schema.Format == "" {
					if n < math.MinInt32 || n > math.MaxInt32 {
						if schema.Format == "int32" {
							return Candidate{}, fmt.Errorf("parameter %q: value %d does not fit int32", param.Name, n)
						}
						return Value(n), nil
					}
					return Value(int32(n)), nil
				}
				return Value(n), nil
			case schema.Type.Is("number"):
				f, err := toFloat64(v)
				if err != nil {
					return Candidate{}, fmt.Errorf("parameter %q: %w", param.Name, err)
				}
				return Value(f), nil
			}
		}
	}
	return Value(v), nil
}

func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int8:
		return int64(n), nil
	case int16:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint:
		return int64(n), nil
	case uint8:
		return int64(n), nil
	case uint16:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	case float32:
		return int64(n), nil
	case float64:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("expected an integer candidate, got %T", v)
	}
}

func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case float32:
		return float64(n), nil
	case float64:
		return n, nil
	default:
		return 0, fmt.Errorf("expected a number candidate, got %T", v)
	}
}
