package openapi3filter

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Strict query parameter attribution.
//
// The loose decoder decodes every parameter independently against the whole
// request query string. Two exploded object parameters that declare the same
// property name therefore both consume the same raw query item, and an
// exploded object property can silently shadow an explicitly named parameter.
//
// In strict mode the whole operation's query parameters are first compiled
// into an occupation plan; the raw query is then attributed item by item so
// that every raw query item belongs to at most one parameter. Conflicts that
// are known without looking at the request are reported while compiling the
// plan; conflicts that only the actual input can reveal are reported against
// the raw query items together with every candidate parameter.

// QueryItem is a single, originally encoded query item.
type QueryItem struct {
	// Index is the zero-based ordinal of the item in the raw query string,
	// so repeated items keep their position.
	Index int
	// Raw is the original, still percent-encoded item as it appeared between
	// the '&' separators of the query string.
	Raw string
	// Key is the percent-decoded key.
	Key string
	// Value is the percent-decoded value.
	Value string
}

// AmbiguousQueryItem is a raw query item that two or more open parameter
// sets could claim; attribution refuses to guess.
type AmbiguousQueryItem struct {
	QueryItem
	// Candidates are the names of every parameter that could own the item,
	// in parameter declaration order.
	Candidates []string
}

// QueryAttributionReport describes how raw query items were (or could not be)
// attributed to operation parameters.
type QueryAttributionReport struct {
	// Ambiguous lists items that matched more than one open parameter set.
	Ambiguous []AmbiguousQueryItem
	// Unconsumed lists items that no operation parameter can own.
	Unconsumed []QueryItem
}

// QueryAttributionError reports input-dependent attribution failures. It is
// returned when ambiguous items exist, or when unconsumed items exist and the
// RejectUnconsumedQueryParams option is enabled.
type QueryAttributionError struct {
	Ambiguous  []AmbiguousQueryItem
	Unconsumed []QueryItem
}

var _ error = (*QueryAttributionError)(nil)

func (e *QueryAttributionError) Error() string {
	var b strings.Builder
	if len(e.Ambiguous) > 0 {
		b.WriteString("query items could belong to more than one parameter:")
		for _, item := range e.Ambiguous {
			fmt.Fprintf(&b, " %s (candidates: %s);", item.Raw, strings.Join(item.Candidates, ", "))
		}
	}
	if len(e.Unconsumed) > 0 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString("query items are not declared by any parameter:")
		for _, item := range e.Unconsumed {
			fmt.Fprintf(&b, " %s;", item.Raw)
		}
	}
	return strings.TrimSuffix(b.String(), ";")
}

// ParameterPlanConflict is a statically known collision between two query
// parameters over the same query key.
type ParameterPlanConflict struct {
	// Parameters are the two conflicting parameter names.
	Parameters [2]string
	// Key is the fully determined query key (or deepObject prefix) both
	// parameters claim.
	Key string
}

// ParameterPlanError reports collisions that are known from the operation
// specification alone, independently of any request.
type ParameterPlanError struct {
	Conflicts []ParameterPlanConflict
}

var _ error = (*ParameterPlanError)(nil)

func (e *ParameterPlanError) Error() string {
	parts := make([]string, 0, len(e.Conflicts))
	for _, c := range e.Conflicts {
		parts = append(parts, fmt.Sprintf("query key %q is statically claimed by parameters %q and %q",
			c.Key, c.Parameters[0], c.Parameters[1]))
	}
	return strings.Join(parts, "; ")
}

// queryClaimKind describes how a query parameter occupies query keys.
type queryClaimKind int

const (
	// claimExact is an ordinary named parameter: only the raw key equal to
	// the parameter name is occupied (primitives, arrays, non-exploded form
	// objects, content parameters, ...).
	claimExact queryClaimKind = iota
	// claimFormProps is an exploded form object with a closed property set:
	// each declared property name is an exact query key.
	claimFormProps
	// claimDeepProps is a deepObject parameter with a closed property set:
	// every key prefixed with "<name>[<prop>]" is occupied.
	claimDeepProps
	// claimOpenForm is an exploded form object whose additionalProperties
	// allow arbitrary keys: every unclaimed key may belong to it.
	claimOpenForm
	// claimOpenDeep is a deepObject parameter whose additionalProperties
	// allow arbitrary segments: every key prefixed with "<name>[" may belong.
	claimOpenDeep
)

type queryClaim struct {
	index int
	param *openapi3.Parameter
	name  string
	kind  queryClaimKind
	props map[string]struct{}
}

// staticAnchor is a fully determined piece of query-key space.
type staticAnchor struct {
	owner  int
	key    string
	prefix bool
}

type queryParameterPlan struct {
	claims []queryClaim
	// exact maps a fully determined query key to its claim index.
	exact map[string]int
	// exactAnchors keeps every fully determined anchor, including duplicate
	// keys from different claims, for static conflict detection.
	exactAnchors []staticAnchor
	// prefixes lists deterministic deepObject prefixes.
	prefixes []staticAnchor
	// open lists claims that may match keys not statically occupied.
	open []queryClaim
}

// compileQueryParameterPlan builds the occupation plan for an operation.
// Path item parameters are included only when the operation does not override
// them (same "in" and name), matching ValidateRequest's parameter precedence.
func compileQueryParameterPlan(pathItemParams, operationParams openapi3.Parameters) (*queryParameterPlan, error) {
	plan := &queryParameterPlan{exact: map[string]int{}}
	add := func(param *openapi3.Parameter) {
		plan.claims = append(plan.claims, buildQueryClaim(len(plan.claims), param))
	}

	for _, ref := range pathItemParams {
		param := ref.Value
		if param.In != openapi3.ParameterInQuery {
			continue
		}
		if operationParams != nil && operationParams.GetByInAndName(param.In, param.Name) != nil {
			continue
		}
		add(param)
	}
	for _, ref := range operationParams {
		if ref.Value.In != openapi3.ParameterInQuery {
			continue
		}
		add(ref.Value)
	}

	for _, claim := range plan.claims {
		switch claim.kind {
		case claimExact:
			plan.registerExact(claim.name, claim.index)
		case claimFormProps:
			for key := range claim.props {
				plan.registerExact(key, claim.index)
			}
		case claimDeepProps:
			props := make([]string, 0, len(claim.props))
			for prop := range claim.props {
				props = append(props, prop)
			}
			sort.Strings(props)
			for _, prop := range props {
				plan.prefixes = append(plan.prefixes, staticAnchor{
					owner:  claim.index,
					key:    claim.name + "[" + prop + "]",
					prefix: true,
				})
			}
		case claimOpenForm, claimOpenDeep:
			plan.open = append(plan.open, claim)
		}
	}

	var conflicts []ParameterPlanConflict
	anchors := plan.staticAnchors()
	for i := 0; i < len(anchors); i++ {
		for j := i + 1; j < len(anchors); j++ {
			if anchors[i].owner == anchors[j].owner || !anchorsOverlap(anchors[i], anchors[j]) {
				continue
			}
			a := plan.claims[anchors[i].owner]
			b := plan.claims[anchors[j].owner]
			conflicts = append(conflicts, ParameterPlanConflict{
				Parameters: [2]string{a.name, b.name},
				Key:        overlappingKey(anchors[i], anchors[j]),
			})
		}
	}
	if len(conflicts) > 0 {
		sort.Slice(conflicts, func(i, j int) bool {
			if conflicts[i].Key != conflicts[j].Key {
				return conflicts[i].Key < conflicts[j].Key
			}
			return conflicts[i].Parameters[0] < conflicts[j].Parameters[0]
		})
		return nil, &ParameterPlanError{Conflicts: conflicts}
	}
	return plan, nil
}

func (plan *queryParameterPlan) registerExact(key string, owner int) {
	plan.exact[key] = owner
	plan.exactAnchors = append(plan.exactAnchors, staticAnchor{owner: owner, key: key})
}

// overlappingKey returns a concrete key shared by two overlapping anchors.
func overlappingKey(a, b staticAnchor) string {
	switch {
	case !a.prefix && !b.prefix:
		return a.key
	case a.prefix && b.prefix:
		if len(a.key) <= len(b.key) {
			return a.key
		}
		return b.key
	case a.prefix:
		return b.key
	default:
		return a.key
	}
}

func (plan *queryParameterPlan) staticAnchors() []staticAnchor {
	anchors := make([]staticAnchor, 0, len(plan.exactAnchors)+len(plan.prefixes))
	anchors = append(anchors, plan.exactAnchors...)
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].key != anchors[j].key {
			return anchors[i].key < anchors[j].key
		}
		return anchors[i].owner < anchors[j].owner
	})
	anchors = append(anchors, plan.prefixes...)
	return anchors
}

// anchorsOverlap reports whether the key spaces of two static anchors
// intersect. Exact anchors occupy a single key; prefix anchors occupy every
// key starting with the prefix (e.g. a deepObject "filter[id]" segment).
func anchorsOverlap(a, b staticAnchor) bool {
	switch {
	case !a.prefix && !b.prefix:
		return a.key == b.key
	case a.prefix && b.prefix:
		return strings.HasPrefix(a.key, b.key) || strings.HasPrefix(b.key, a.key)
	default:
		exact, prefix := a.key, b.key
		if b.prefix {
			exact, prefix = b.key, a.key
		}
		return strings.HasPrefix(exact, prefix)
	}
}

func buildQueryClaim(index int, param *openapi3.Parameter) queryClaim {
	claim := queryClaim{index: index, param: param, name: param.Name, kind: claimExact}

	sm, err := param.SerializationMethod()
	if err != nil || param.Schema == nil || param.Schema.Value == nil {
		return claim
	}

	schema := param.Schema.Value
	if !schema.Type.Is("object") {
		return claim
	}

	switch sm.Style {
	case "deepObject":
		props, open := collectObjectShape(param.Schema)
		if open {
			claim.kind = claimOpenDeep
		} else {
			claim.kind = claimDeepProps
			claim.props = props
		}
	case "form":
		if !sm.Explode {
			return claim
		}
		props, open := collectObjectShape(param.Schema)
		if open {
			claim.kind = claimOpenForm
		} else {
			claim.kind = claimFormProps
			claim.props = props
		}
	}
	return claim
}

// collectObjectShape returns the statically known root property names of an
// object schema and whether the schema accepts arbitrary additional
// properties (allOf schemas are merged in). A schema whose object shape cannot
// be determined statically (anyOf/oneOf branches) is treated as open.
func collectObjectShape(schemaRef *openapi3.SchemaRef) (map[string]struct{}, bool) {
	props := map[string]struct{}{}
	return props, collectObjectShapeInto(schemaRef, props)
}

func collectObjectShapeInto(schemaRef *openapi3.SchemaRef, props map[string]struct{}) bool {
	if schemaRef == nil || schemaRef.Value == nil {
		return false
	}
	schema := schemaRef.Value
	if decodesAdditionalProperties(schema) {
		return true
	}
	for _, sub := range schema.AllOf {
		if collectObjectShapeInto(sub, props) {
			return true
		}
	}
	for _, sub := range schema.AnyOf {
		if sub.Value != nil && sub.Value.Type.Is("object") {
			return true
		}
	}
	for _, sub := range schema.OneOf {
		if sub.Value != nil && sub.Value.Type.Is("object") {
			return true
		}
	}
	for name := range schema.Properties {
		props[name] = struct{}{}
	}
	return false
}

// rawQueryItem is one raw query item together with its decoded key/value.
type rawQueryItem struct {
	QueryItem
}

// parseRawQuery splits a query string into its original items, preserving
// both the percent-encoded form (for faithful error reporting) and the
// decoded key/value used for attribution.
func parseRawQuery(raw string) []rawQueryItem {
	if raw == "" {
		return nil
	}
	var items []rawQueryItem
	for index, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		key, value, found := strings.Cut(pair, "=")
		if !found {
			value = ""
		}
		items = append(items, rawQueryItem{QueryItem: QueryItem{
			Index: index,
			Raw:   pair,
			Key:   queryUnescape(key),
			Value: queryUnescape(value),
		}})
	}
	return items
}

func queryUnescape(v string) string {
	if decoded, err := url.QueryUnescape(v); err == nil {
		return decoded
	}
	return v
}

// attributeQuery assigns every raw query item to at most one parameter.
func attributeQuery(plan *queryParameterPlan, items []rawQueryItem) (map[string]url.Values, *QueryAttributionReport) {
	values := make(map[string]url.Values, len(plan.claims))
	report := &QueryAttributionReport{}

	assign := func(claim queryClaim, key, value string) {
		q := values[claim.name]
		if q == nil {
			q = url.Values{}
			values[claim.name] = q
		}
		q.Add(key, value)
	}

	for _, item := range items {
		if owner, ok := plan.staticOwner(item.Key); ok {
			assign(plan.claims[owner], item.Key, item.Value)
			continue
		}

		var candidates []queryClaim
		for _, claim := range plan.open {
			switch claim.kind {
			case claimOpenForm:
				candidates = append(candidates, claim)
			case claimOpenDeep:
				if strings.HasPrefix(item.Key, claim.name+"[") {
					candidates = append(candidates, claim)
				}
			}
		}
		switch len(candidates) {
		case 0:
			report.Unconsumed = append(report.Unconsumed, item.QueryItem)
		case 1:
			assign(candidates[0], item.Key, item.Value)
		default:
			names := make([]string, len(candidates))
			for i, claim := range candidates {
				names[i] = claim.name
			}
			report.Ambiguous = append(report.Ambiguous, AmbiguousQueryItem{
				QueryItem:  item.QueryItem,
				Candidates: names,
			})
		}
	}
	return values, report
}

// staticOwner resolves the deterministic owner of a decoded query key from
// the exact anchors and closed deepObject prefixes.
func (plan *queryParameterPlan) staticOwner(key string) (int, bool) {
	if owner, ok := plan.exact[key]; ok {
		return owner, true
	}
	for _, anchor := range plan.prefixes {
		if strings.HasPrefix(key, anchor.key) {
			return anchor.owner, true
		}
	}
	return 0, false
}
