package openapi3plan

import (
	"fmt"
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
)

// DomainConfig overrides or completes the finite candidate domain of one
// operation parameter.
//
// At least one of Explicit, IncludeNull or IncludeAbsent must yield a
// non-empty domain: string/integer schemas without enum are infinite as far
// as the planner can tell, so callers planning such parameters must provide
// Explicit candidates.
type DomainConfig struct {
	// Explicit, when non-nil, replaces schema-derived concrete candidates
	// entirely with the supplied finite list. Order does not matter: values
	// are de-duplicated and sorted deterministically.
	Explicit []any

	// IncludeNull adds a present-but-null candidate.
	// Defaults to true only when the schema explicitly permits null
	// (3.0 nullable:true, an enum null member, or a 3.1 "null" type).
	IncludeNull *bool
	// IncludeAbsent adds a parameter-missing candidate.
	// Defaults to true for optional parameters and false for required ones.
	IncludeAbsent *bool
}

// paramDomain is one parameter together with its sorted, de-duplicated
// candidates.
type paramDomain struct {
	param      *openapi3.Parameter
	candidates []Candidate
	keys       []string
}

func mergedParameters(op *openapi3.Operation, pathItem *openapi3.PathItem) []*openapi3.Parameter {
	params := make([]*openapi3.Parameter, 0, len(op.Parameters)+len(pathItem.Parameters))
	indexByKey := make(map[string]int)
	add := func(p *openapi3.Parameter) {
		if p == nil {
			return
		}
		key := p.In + ":" + p.Name
		if idx, ok := indexByKey[key]; ok {
			params[idx] = p
			return
		}
		indexByKey[key] = len(params)
		params = append(params, p)
	}
	// Path-item parameters provide defaults; operation parameters override.
	for _, ref := range pathItem.Parameters {
		add(ref.Value)
	}
	for _, ref := range op.Parameters {
		add(ref.Value)
	}
	sortParams(params)
	return params
}

func sortParams(params []*openapi3.Parameter) {
	rank := func(in string) int {
		switch in {
		case openapi3.ParameterInPath:
			return 0
		case openapi3.ParameterInQuery:
			return 1
		case openapi3.ParameterInHeader:
			return 2
		case openapi3.ParameterInCookie:
			return 3
		default:
			return 4
		}
	}
	slices.SortFunc(params, func(a, b *openapi3.Parameter) int {
		if ra, rb := rank(a.In), rank(b.In); ra != rb {
			return ra - rb
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
}

// domainFor extracts the finite domain of one parameter.
func domainFor(param *openapi3.Parameter, cfg DomainConfig) (*paramDomain, error) {
	byKey := make(map[string]Candidate)
	add := func(c Candidate) error {
		k, err := c.key()
		if err != nil {
			return fmt.Errorf("parameter %q: %w", param.Name, err)
		}
		byKey[k] = c
		return nil
	}

	for _, v := range cfg.Explicit {
		c, err := canonicalCandidate(param, v)
		if err != nil {
			return nil, err
		}
		if err := add(c); err != nil {
			return nil, err
		}
	}

	if cfg.Explicit == nil {
		derived, err := deriveConcrete(param)
		if err != nil {
			return nil, err
		}
		for _, c := range derived {
			c, err = canonicalCandidate(param, c.Value)
			if err != nil {
				return nil, err
			}
			if err := add(c); err != nil {
				return nil, err
			}
		}
	}

	includeNull := schemaPermitsNull(param)
	if cfg.IncludeNull != nil {
		includeNull = *cfg.IncludeNull
	}
	if includeNull {
		if err := add(Null()); err != nil {
			return nil, err
		}
	}

	includeAbsent := !param.Required
	if cfg.IncludeAbsent != nil {
		includeAbsent = *cfg.IncludeAbsent
	}
	if includeAbsent {
		if err := add(Absent()); err != nil {
			return nil, err
		}
	}

	if len(byKey) == 0 {
		return nil, fmt.Errorf("parameter %q has an empty domain: provide explicit candidates (its schema has no finite enum/boolean domain)", param.Name)
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	candidates := make([]Candidate, len(keys))
	for i, k := range keys {
		candidates[i] = byKey[k]
	}
	return &paramDomain{param: param, candidates: candidates, keys: keys}, nil
}

// deriveConcrete extracts concrete values allowed without caller input.
// Only closed, finite shapes are extracted: enum members and boolean
// true/false. Unbounded integer/string/number schemas intentionally produce no
// candidates, forcing an explicit domain instead of enumerating an infinite
// range.
func deriveConcrete(param *openapi3.Parameter) ([]Candidate, error) {
	if param.Schema == nil || param.Schema.Value == nil {
		if param.Content != nil {
			return nil, fmt.Errorf("parameter %q uses content media types; provide explicit candidates", param.Name)
		}
		// Schemaless parameters accept anything: infinite, so require explicit.
		return nil, fmt.Errorf("parameter %q has no schema; provide explicit candidates", param.Name)
	}
	schema := param.Schema.Value
	if schema.Always != nil && !*schema.Always {
		return nil, fmt.Errorf("parameter %q schema is boolean false and accepts no values", param.Name)
	}

	// Finite enum list: the only way to derive concrete values for numbers,
	// strings and mixed/multi-type schemas.
	if len(schema.Enum) > 0 {
		out := make([]Candidate, 0, len(schema.Enum))
		for _, v := range schema.Enum {
			if v == nil {
				continue // null membership is handled via IncludeNull.
			}
			if err := checkConcrete(param, v); err != nil {
				return nil, err
			}
			out = append(out, Value(v))
		}
		return out, nil
	}

	if schema.Type == nil {
		return nil, fmt.Errorf("parameter %q schema has no finite enum/boolean domain; provide explicit candidates", param.Name)
	}

	// Boolean is the only non-enum primitive with a finite domain.
	if schema.Type.IsSingle() && schema.Type.Is("boolean") {
		return []Candidate{Value(false), Value(true)}, nil
	}

	return nil, fmt.Errorf("parameter %q schema type %v is not finite (only enum and boolean are derived); provide explicit candidates", param.Name, schema.Type.Slice())
}

// checkConcrete rejects shapes the offline serializer cannot encode and that
// cannot serve as stable pair keys: objects, arrays, and non-JSON values.
// Enums containing such values require an explicit, serializer-aware setup, so
// they are rejected rather than silently mis-encoded.
func checkConcrete(param *openapi3.Parameter, v any) error {
	switch v.(type) {
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return nil
	case []any:
		return fmt.Errorf("parameter %q enum contains an array, which is not supported by the offline planner", param.Name)
	case map[string]any:
		return fmt.Errorf("parameter %q enum contains an object, which is not supported by the offline planner", param.Name)
	default:
		return fmt.Errorf("parameter %q enum contains an unsupported value type %T", param.Name, v)
	}
}

// schemaPermitsNull reports whether null is part of the declared schema.
func schemaPermitsNull(param *openapi3.Parameter) bool {
	if param.Schema == nil || param.Schema.Value == nil {
		return false
	}
	schema := param.Schema.Value
	if schema.Nullable {
		return true
	}
	if schema.Type != nil && schema.Type.IncludesNull() {
		return true
	}
	for _, v := range schema.Enum {
		if v == nil {
			return true
		}
	}
	// An empty wire segment decodes to nil for non-string primitives and is
	// accepted precisely when allowEmptyValue is set, which is the only way to
	// transmit a null-ish value for these parameter types.
	if param.AllowEmptyValue && schema.Type != nil && !schema.Type.Is("string") {
		return true
	}
	return false
}
