package openapi3pairwise

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"
)

// DomainFor derives a finite candidate domain for one parameter.
//
// Concrete values come from explicit when it is non-nil, otherwise from the
// parameter schema's enum, otherwise — for boolean schemas — {false, true}.
// String, integer and number schemas without an enum and without explicit
// values are rejected: enumerating them would mean enumerating an unbounded
// domain.
//
// The Absent candidate is added when the parameter is not required, and the
// Null candidate when the schema is nullable (OpenAPI 3.0 nullable or an
// OpenAPI 3.1 type list including "null"). The returned candidates are
// deduplicated and canonically ordered.
func DomainFor(param *openapi3.Parameter, explicit []any) ([]Candidate, error) {
	var concretes []any
	switch {
	case explicit != nil:
		concretes = explicit
	case param.Schema != nil && param.Schema.Value != nil:
		schema := param.Schema.Value
		switch {
		case len(schema.Enum) > 0:
			concretes = schema.Enum
		case schema.Type != nil && schema.Type.Is("boolean"):
			concretes = []any{false, true}
		default:
			return nil, fmt.Errorf("openapi3pairwise: parameter %q: no finite domain (no explicit values, no enum, not a boolean); refusing to enumerate an unbounded domain", param.Name)
		}
	default:
		return nil, fmt.Errorf("openapi3pairwise: parameter %q has no schema; provide explicit values", param.Name)
	}

	var out []Candidate
	if !param.Required && param.In != openapi3.ParameterInPath {
		out = append(out, Absent())
	}
	if param.Schema != nil && isNullable(param.Schema.Value) {
		out = append(out, Null())
	}
	for _, v := range concretes {
		if !isScalar(v) {
			return nil, fmt.Errorf("openapi3pairwise: parameter %q: candidate value of type %T is not a scalar", param.Name, v)
		}
		out = append(out, Value(v))
	}
	return dedupeSorted(out), nil
}

func isNullable(schema *openapi3.Schema) bool {
	if schema == nil {
		return false
	}
	if schema.Nullable {
		return true
	}
	return schema.Type != nil && schema.Type.IncludesNull()
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, bool, json.Number,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	}
	return false
}

// dedupeSorted sorts candidates by canonical key and removes duplicates.
func dedupeSorted(candidates []Candidate) []Candidate {
	seen := make(map[string]struct{}, len(candidates))
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		k := c.key()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}
