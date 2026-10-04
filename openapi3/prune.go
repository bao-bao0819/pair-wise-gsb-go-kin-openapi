package openapi3

import (
	"fmt"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"
)

// RemovedLink describes a link that PruneOperations dropped because it points
// at an operation that is not part of the selection.
type RemovedLink struct {
	// Source is the location of the link in the original document, e.g.
	// "paths./pets/get -> responses.200 -> links.getPet".
	Source string
	// Name is the key of the link in its Links map.
	Name string
	// OperationID is the link's operationId target, when set.
	OperationID string
	// OperationRef is the link's operationRef target, when set.
	OperationRef string
}

// RenamedComponent describes a component that PruneOperations stored under a
// different name because its preferred name was already taken by a component
// from another document.
type RenamedComponent struct {
	// Collection is the components collection, e.g. "schemas".
	Collection string
	// Name is the component's name in the pruned document.
	Name string
	// Source is the canonical URI of the original component.
	Source string
}

// PruneReport describes what PruneOperations dropped or renamed while
// building the pruned document.
type PruneReport struct {
	// RemovedLinks lists links that were removed because they target an
	// operation outside the selection. The operation set is never expanded
	// to keep a link.
	RemovedLinks []RemovedLink
	// RenamedComponents lists components whose name changed, e.g. two
	// components named "Node" coming from different files.
	RenamedComponents []RenamedComponent
}

// PruneOperations returns a new, self-contained document that keeps only the
// operations named by operationIDs (looked up in doc's paths and webhooks),
// together with everything those operations need:
//
//   - path-level parameters, and the effective security and servers (root,
//     path-item and operation levels are preserved as written);
//   - every component reachable from the kept operations, including through
//     schema cycles, callbacks, links and discriminator mappings.
//
// All references in the returned document are internal ("#/components/..."),
// so it loads and validates without access to the original external files.
// Components with the same name coming from different files never overwrite
// each other: later ones are renamed deterministically (see PruneReport).
// Links that target operations outside the selection are removed and listed
// in the report. Unselected operations and unreachable components are
// dropped. The input document is not modified.
//
// The loader is the one the document was loaded with; it is used to resolve
// relative references the same way they were resolved at load time. When a
// reference cannot be resolved, the returned error carries the full chain of
// references that led to it, and no document is returned.
func PruneOperations(doc *T, loader *Loader, operationIDs []string) (*T, *PruneReport, error) {
	if doc == nil {
		return nil, nil, fmt.Errorf("openapi3: prune: nil document")
	}
	if len(operationIDs) == 0 {
		return nil, nil, fmt.Errorf("openapi3: prune: no operation IDs selected")
	}
	if loader == nil {
		loader = NewLoader()
	}

	p := &pruner{
		doc:         doc,
		loader:      loader,
		selected:    make(map[string]struct{}, len(operationIDs)),
		selectedOp:  make(map[string]struct{}),
		names:       make(map[string]map[string]string),
		used:        make(map[string]map[string]string),
		schemeNames: make(map[string]struct{}),
		usedTags:    make(map[string]struct{}),
		report:      &PruneReport{},
	}
	for _, id := range operationIDs {
		p.selected[id] = struct{}{}
	}

	// Pass 1: locate the selected operations and record their canonical
	// identities, so links targeting them can be told apart from links
	// targeting operations that stay behind.
	found := make(map[string]struct{}, len(operationIDs))
	registerOps := func(fragPrefix, name string, pathItem *PathItem) {
		if pathItem == nil {
			return
		}
		for _, entry := range pathItem.operationEntries() {
			op := entry.operation
			if op == nil {
				continue
			}
			if _, ok := p.selected[op.OperationID]; !ok {
				continue
			}
			found[op.OperationID] = struct{}{}
			u := copyURI(doc.url)
			if u == nil {
				u = new(url.URL)
			}
			u.Fragment = fragPrefix + escapeJSONPointerToken(name) + entry.pointerSuffix
			p.selectedOp[canonicalRefKey(u)] = struct{}{}
			// A path item brought in by $ref is also addressable through the
			// referenced document; accept that spelling in operationRefs too.
			if pathItem.Ref != "" {
				if u2, err := resolveRefString(loader, pathItem.Ref, doc.url); err == nil {
					u2.Fragment = strings.TrimSuffix(u2.Fragment, "/") + entry.pointerSuffix
					p.selectedOp[canonicalRefKey(u2)] = struct{}{}
				}
			}
		}
	}
	if doc.Paths != nil {
		for _, name := range componentNames(doc.Paths.Map()) {
			registerOps("/paths/", name, doc.Paths.Map()[name])
		}
	}
	for _, name := range componentNames(doc.Webhooks) {
		registerOps("/webhooks/", name, doc.Webhooks[name])
	}
	var missing []string
	for _, id := range operationIDs {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("openapi3: prune: operation IDs not found in document: %s", strings.Join(missing, ", "))
	}

	// Pass 2: copy the selected operations and everything reachable from them.
	out := &T{
		OpenAPI:           doc.OpenAPI,
		Extensions:        maps.Clone(doc.Extensions),
		Info:              cloneInfo(doc.Info),
		Security:          cloneSecurityRequirements(doc.Security),
		Servers:           cloneServers(doc.Servers),
		ExternalDocs:      cloneExternalDocs(doc.ExternalDocs),
		JSONSchemaDialect: doc.JSONSchemaDialect,
		Paths:             NewPaths(),
	}
	p.out = out
	p.collectSecurityNames(doc.Security)

	if doc.Paths != nil {
		out.Paths.Extensions = maps.Clone(doc.Paths.Extensions)
		for _, name := range componentNames(doc.Paths.Map()) {
			pathItem := doc.Paths.Map()[name]
			if pathItem == nil {
				continue
			}
			np, kept, err := p.copyPathItem(pathItem, doc.url, "paths."+name, p.keepSelected)
			if err != nil {
				return nil, nil, err
			}
			if kept > 0 {
				out.Paths.Set(name, np)
			}
		}
	}
	if len(doc.Webhooks) > 0 {
		webhooks := make(map[string]*PathItem, len(doc.Webhooks))
		for _, name := range componentNames(doc.Webhooks) {
			pathItem := doc.Webhooks[name]
			if pathItem == nil {
				continue
			}
			np, kept, err := p.copyPathItem(pathItem, doc.url, "webhooks."+name, p.keepSelected)
			if err != nil {
				return nil, nil, err
			}
			if kept > 0 {
				webhooks[name] = np
			}
		}
		if len(webhooks) > 0 {
			out.Webhooks = webhooks
		}
	}

	// Security schemes are referenced by name from security requirements;
	// pull in exactly the schemes the kept requirements use, and rewrite the
	// requirement keys if a scheme had to be renamed.
	if err := p.registerSecuritySchemes(); err != nil {
		return nil, nil, err
	}

	// Keep only the tags the kept operations use.
	for _, tag := range doc.Tags {
		if tag == nil {
			continue
		}
		if _, ok := p.usedTags[tag.Name]; !ok {
			continue
		}
		nt := *tag
		nt.Extensions = maps.Clone(tag.Extensions)
		nt.ExternalDocs = cloneExternalDocs(tag.ExternalDocs)
		out.Tags = append(out.Tags, &nt)
	}

	for _, fixup := range p.fixups {
		fixup()
	}
	if componentsEmpty(out.Components) {
		out.Components = nil
	}
	return out, p.report, nil
}

// pruner holds the state of a single PruneOperations run.
type pruner struct {
	doc    *T
	loader *Loader

	selected   map[string]struct{} // operation IDs to keep
	selectedOp map[string]struct{} // canonical URIs of the kept operations

	out *T

	names  map[string]map[string]string // collection -> canonical URI -> component name
	used   map[string]map[string]string // collection -> component name -> canonical URI
	fixups []func()                     // links component values once cyclic copies settle

	schemeNames map[string]struct{} // security scheme names used by kept requirements
	usedTags    map[string]struct{} // tag names used by kept operations

	chain  []string // descent path, for error reporting
	report *PruneReport
}

func (p *pruner) keepSelected(op *Operation) bool {
	if op == nil {
		return false
	}
	_, ok := p.selected[op.OperationID]
	return ok
}

func keepAllOperations(*Operation) bool { return true }

func (p *pruner) pushChain(entry string) { p.chain = append(p.chain, entry) }

func (p *pruner) popChain() { p.chain = p.chain[:len(p.chain)-1] }

func (p *pruner) chainString() string {
	if len(p.chain) == 0 {
		return "(document root)"
	}
	return strings.Join(p.chain, " -> ")
}

func (p *pruner) errUnresolved(ref string) error {
	return fmt.Errorf("openapi3: prune: unresolved reference %q\nreference chain: %s", ref, p.chainString())
}

// resolveRefString resolves ref against cur the same way the loader does:
// fragment-only references stay inside the current document, everything else
// goes through the loader's path resolution (including its JoinFunc).
func resolveRefString(loader *Loader, ref string, cur *url.URL) (*url.URL, error) {
	if strings.HasPrefix(ref, "#") {
		u := copyURI(cur)
		if u == nil {
			u = new(url.URL)
		}
		parsed, err := url.Parse(ref)
		if err != nil {
			return nil, fmt.Errorf("cannot parse reference: %q: %w", ref, err)
		}
		u.Fragment = parsed.Fragment
		return u, nil
	}
	return loader.resolvePathWithRef(ref, cur)
}

func (p *pruner) resolveRef(ref string, cur *url.URL) (*url.URL, error) {
	u, err := resolveRefString(p.loader, ref, cur)
	if err != nil {
		return nil, fmt.Errorf("openapi3: prune: cannot resolve reference %q: %w\nreference chain: %s", ref, err, p.chainString())
	}
	return u, nil
}

// canonicalRefKey identifies a component by the document it lives in and its
// JSON pointer fragment inside that document.
func canonicalRefKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}

func escapeJSONPointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// preferredComponentName derives a component name from its canonical URI: the
// last fragment segment for pointer references, the file base name for
// whole-document references.
func preferredComponentName(u *url.URL) string {
	if frag := u.Fragment; frag != "" && frag != "/" {
		return unescapeRefString(frag[strings.LastIndex(frag, "/")+1:])
	}
	raw := path.Base(u.Path)
	for ext := path.Ext(raw); ext != ""; ext = path.Ext(raw) {
		raw = strings.TrimSuffix(raw, ext)
	}
	return raw
}

// assignName registers key under a unique name in the collection, deriving
// the name from base and suffixing it deterministically on collisions.
func (p *pruner) assignName(collection, base, key string) string {
	rawBase := base
	if base = InvalidIdentifierCharRegExp.ReplaceAllString(base, "_"); base == "" {
		base = "Component"
	}
	used := p.used[collection]
	if used == nil {
		used = make(map[string]string)
		p.used[collection] = used
	}
	name := base
	for i := 2; ; i++ {
		owner, taken := used[name]
		if !taken || owner == key {
			break
		}
		name = fmt.Sprintf("%s_%d", base, i)
	}
	used[name] = key
	if name != rawBase {
		p.report.RenamedComponents = append(p.report.RenamedComponents, RenamedComponent{
			Collection: collection,
			Name:       name,
			Source:     key,
		})
	}
	return name
}

// componentName returns the name registered for the component identified by
// u, registering a fresh one (isNew true) on first encounter. Registering
// before the component body is copied is what makes reference cycles
// terminate: a cyclic reference finds the name already taken.
func (p *pruner) componentName(collection string, u *url.URL, nameHint string) (name, key string, isNew bool) {
	key = canonicalRefKey(u)
	if p.names[collection] == nil {
		p.names[collection] = make(map[string]string)
	}
	if name, ok := p.names[collection][key]; ok {
		return name, key, false
	}
	base := nameHint
	if base == "" {
		base = preferredComponentName(u)
	}
	name = p.assignName(collection, base, key)
	p.names[collection][key] = name
	return name, key, true
}

// sourceDoc returns the loaded document cur points into, used to follow
// by-name references (discriminator mappings, security schemes) that the
// loader deliberately left unresolved.
func (p *pruner) sourceDoc(cur *url.URL) *T {
	docKey := func(u *url.URL) string {
		if u == nil {
			return ""
		}
		c := *u
		c.Fragment = ""
		return c.String()
	}
	if docKey(cur) == docKey(p.doc.url) {
		return p.doc
	}
	if doc, ok := p.loader.visitedDocuments[docKey(cur)]; ok {
		return doc
	}
	return nil
}

func (p *pruner) components() *Components {
	if p.out.Components == nil {
		p.out.Components = &Components{}
	}
	return p.out.Components
}

func componentsEmpty(c *Components) bool {
	return c == nil ||
		len(c.Schemas) == 0 &&
			len(c.Parameters) == 0 &&
			len(c.Headers) == 0 &&
			len(c.RequestBodies) == 0 &&
			len(c.Responses) == 0 &&
			len(c.SecuritySchemes) == 0 &&
			len(c.Examples) == 0 &&
			len(c.Links) == 0 &&
			len(c.Callbacks) == 0 &&
			len(c.Extensions) == 0
}

func (p *pruner) componentSchemas() Schemas {
	c := p.components()
	if c.Schemas == nil {
		c.Schemas = make(Schemas)
	}
	return c.Schemas
}

func (p *pruner) componentParameters() ParametersMap {
	c := p.components()
	if c.Parameters == nil {
		c.Parameters = make(ParametersMap)
	}
	return c.Parameters
}

func (p *pruner) componentHeaders() Headers {
	c := p.components()
	if c.Headers == nil {
		c.Headers = make(Headers)
	}
	return c.Headers
}

func (p *pruner) componentRequestBodies() RequestBodies {
	c := p.components()
	if c.RequestBodies == nil {
		c.RequestBodies = make(RequestBodies)
	}
	return c.RequestBodies
}

func (p *pruner) componentResponses() ResponseBodies {
	c := p.components()
	if c.Responses == nil {
		c.Responses = make(ResponseBodies)
	}
	return c.Responses
}

func (p *pruner) componentSecuritySchemes() SecuritySchemes {
	c := p.components()
	if c.SecuritySchemes == nil {
		c.SecuritySchemes = make(SecuritySchemes)
	}
	return c.SecuritySchemes
}

func (p *pruner) componentExamples() Examples {
	c := p.components()
	if c.Examples == nil {
		c.Examples = make(Examples)
	}
	return c.Examples
}

func (p *pruner) componentLinks() Links {
	c := p.components()
	if c.Links == nil {
		c.Links = make(Links)
	}
	return c.Links
}

func (p *pruner) componentCallbacks() Callbacks {
	c := p.components()
	if c.Callbacks == nil {
		c.Callbacks = make(Callbacks)
	}
	return c.Callbacks
}

// ensureSchemaComponent registers the schema identified by u and returns its
// component name. The body is copied with u as its base document, so nested
// references resolve against the document the schema actually lives in.
func (p *pruner) ensureSchemaComponent(u *url.URL, value *Schema) (string, error) {
	name, key, isNew := p.componentName("schemas", u, "")
	if !isNew {
		return name, nil
	}
	slot := &SchemaRef{}
	p.componentSchemas()[name] = slot
	p.pushChain(fmt.Sprintf("#/components/schemas/%s (from %s)", name, key))
	body, err := p.schema(value, u)
	p.popChain()
	if err != nil {
		return "", err
	}
	slot.Value = body
	return name, nil
}

// schemaRef copies a schema reference, rewriting a $ref to point at the
// pruned document's components and registering the target on first sight.
func (p *pruner) schemaRef(src *SchemaRef, cur *url.URL) (*SchemaRef, error) {
	if src == nil {
		return nil, nil
	}
	if src.Ref == "" {
		if src.Value == nil {
			return nil, nil
		}
		v, err := p.schema(src.Value, cur)
		if err != nil {
			return nil, err
		}
		return &SchemaRef{Value: v, Extensions: maps.Clone(src.Extensions)}, nil
	}
	if src.Value == nil {
		return nil, p.errUnresolved(src.Ref)
	}
	u, err := p.resolveRef(src.Ref, cur)
	if err != nil {
		return nil, err
	}
	name, err := p.ensureSchemaComponent(u, src.Value)
	if err != nil {
		return nil, err
	}
	dst := &SchemaRef{
		Ref:        "#/components/schemas/" + escapeJSONPointerToken(name),
		Extensions: maps.Clone(src.Extensions),
	}
	if slot := p.components().Schemas[name]; slot.Value != nil {
		dst.Value = slot.Value
	} else {
		p.fixups = append(p.fixups, func() { dst.Value = slot.Value })
	}
	return dst, nil
}

func (p *pruner) parameterRef(src *ParameterRef, cur *url.URL) (*ParameterRef, error) {
	if src == nil {
		return nil, nil
	}
	if src.Ref == "" {
		if src.Value == nil {
			return nil, nil
		}
		v, err := p.parameter(src.Value, cur)
		if err != nil {
			return nil, err
		}
		return &ParameterRef{Value: v, Summary: src.Summary, Description: src.Description, Extensions: maps.Clone(src.Extensions)}, nil
	}
	if src.Value == nil {
		return nil, p.errUnresolved(src.Ref)
	}
	u, err := p.resolveRef(src.Ref, cur)
	if err != nil {
		return nil, err
	}
	name, err := p.ensureParameterComponent(u, src.Value)
	if err != nil {
		return nil, err
	}
	dst := &ParameterRef{
		Ref:         "#/components/parameters/" + escapeJSONPointerToken(name),
		Summary:     src.Summary,
		Description: src.Description,
		Extensions:  maps.Clone(src.Extensions),
	}
	if slot := p.components().Parameters[name]; slot.Value != nil {
		dst.Value = slot.Value
	} else {
		p.fixups = append(p.fixups, func() { dst.Value = slot.Value })
	}
	return dst, nil
}

func (p *pruner) headerRef(src *HeaderRef, cur *url.URL) (*HeaderRef, error) {
	if src == nil {
		return nil, nil
	}
	if src.Ref == "" {
		if src.Value == nil {
			return nil, nil
		}
		v, err := p.header(src.Value, cur)
		if err != nil {
			return nil, err
		}
		return &HeaderRef{Value: v, Summary: src.Summary, Description: src.Description, Extensions: maps.Clone(src.Extensions)}, nil
	}
	if src.Value == nil {
		return nil, p.errUnresolved(src.Ref)
	}
	u, err := p.resolveRef(src.Ref, cur)
	if err != nil {
		return nil, err
	}
	name, err := p.ensureHeaderComponent(u, src.Value)
	if err != nil {
		return nil, err
	}
	dst := &HeaderRef{
		Ref:         "#/components/headers/" + escapeJSONPointerToken(name),
		Summary:     src.Summary,
		Description: src.Description,
		Extensions:  maps.Clone(src.Extensions),
	}
	if slot := p.components().Headers[name]; slot.Value != nil {
		dst.Value = slot.Value
	} else {
		p.fixups = append(p.fixups, func() { dst.Value = slot.Value })
	}
	return dst, nil
}

func (p *pruner) requestBodyRef(src *RequestBodyRef, cur *url.URL) (*RequestBodyRef, error) {
	if src == nil {
		return nil, nil
	}
	if src.Ref == "" {
		if src.Value == nil {
			return nil, nil
		}
		v, err := p.requestBody(src.Value, cur)
		if err != nil {
			return nil, err
		}
		return &RequestBodyRef{Value: v, Summary: src.Summary, Description: src.Description, Extensions: maps.Clone(src.Extensions)}, nil
	}
	if src.Value == nil {
		return nil, p.errUnresolved(src.Ref)
	}
	u, err := p.resolveRef(src.Ref, cur)
	if err != nil {
		return nil, err
	}
	name, err := p.ensureRequestBodyComponent(u, src.Value)
	if err != nil {
		return nil, err
	}
	dst := &RequestBodyRef{
		Ref:         "#/components/requestBodies/" + escapeJSONPointerToken(name),
		Summary:     src.Summary,
		Description: src.Description,
		Extensions:  maps.Clone(src.Extensions),
	}
	if slot := p.components().RequestBodies[name]; slot.Value != nil {
		dst.Value = slot.Value
	} else {
		p.fixups = append(p.fixups, func() { dst.Value = slot.Value })
	}
	return dst, nil
}

func (p *pruner) responseRef(src *ResponseRef, cur *url.URL) (*ResponseRef, error) {
	if src == nil {
		return nil, nil
	}
	if src.Ref == "" {
		if src.Value == nil {
			return nil, nil
		}
		v, err := p.response(src.Value, cur)
		if err != nil {
			return nil, err
		}
		return &ResponseRef{Value: v, Summary: src.Summary, Description: src.Description, Extensions: maps.Clone(src.Extensions)}, nil
	}
	if src.Value == nil {
		return nil, p.errUnresolved(src.Ref)
	}
	u, err := p.resolveRef(src.Ref, cur)
	if err != nil {
		return nil, err
	}
	name, err := p.ensureResponseComponent(u, src.Value)
	if err != nil {
		return nil, err
	}
	dst := &ResponseRef{
		Ref:         "#/components/responses/" + escapeJSONPointerToken(name),
		Summary:     src.Summary,
		Description: src.Description,
		Extensions:  maps.Clone(src.Extensions),
	}
	if slot := p.components().Responses[name]; slot.Value != nil {
		dst.Value = slot.Value
	} else {
		p.fixups = append(p.fixups, func() { dst.Value = slot.Value })
	}
	return dst, nil
}

func (p *pruner) exampleRef(src *ExampleRef, cur *url.URL) (*ExampleRef, error) {
	if src == nil {
		return nil, nil
	}
	if src.Ref == "" {
		if src.Value == nil {
			return nil, nil
		}
		return &ExampleRef{
			Value:       cloneExample(src.Value),
			Summary:     src.Summary,
			Description: src.Description,
			Extensions:  maps.Clone(src.Extensions),
		}, nil
	}
	if src.Value == nil {
		return nil, p.errUnresolved(src.Ref)
	}
	u, err := p.resolveRef(src.Ref, cur)
	if err != nil {
		return nil, err
	}
	name := p.ensureExampleComponent(u, src.Value)
	dst := &ExampleRef{
		Ref:         "#/components/examples/" + escapeJSONPointerToken(name),
		Summary:     src.Summary,
		Description: src.Description,
		Extensions:  maps.Clone(src.Extensions),
	}
	if slot := p.components().Examples[name]; slot.Value != nil {
		dst.Value = slot.Value
	} else {
		p.fixups = append(p.fixups, func() { dst.Value = slot.Value })
	}
	return dst, nil
}

func (p *pruner) linkRef(src *LinkRef, cur *url.URL) (*LinkRef, error) {
	if src == nil {
		return nil, nil
	}
	if src.Ref == "" {
		if src.Value == nil {
			return nil, nil
		}
		return &LinkRef{
			Value:       cloneLink(src.Value),
			Summary:     src.Summary,
			Description: src.Description,
			Extensions:  maps.Clone(src.Extensions),
		}, nil
	}
	if src.Value == nil {
		return nil, p.errUnresolved(src.Ref)
	}
	u, err := p.resolveRef(src.Ref, cur)
	if err != nil {
		return nil, err
	}
	name := p.ensureLinkComponent(u, src.Value)
	dst := &LinkRef{
		Ref:         "#/components/links/" + escapeJSONPointerToken(name),
		Summary:     src.Summary,
		Description: src.Description,
		Extensions:  maps.Clone(src.Extensions),
	}
	if slot := p.components().Links[name]; slot.Value != nil {
		dst.Value = slot.Value
	} else {
		p.fixups = append(p.fixups, func() { dst.Value = slot.Value })
	}
	return dst, nil
}

func (p *pruner) callbackRef(src *CallbackRef, cur *url.URL) (*CallbackRef, error) {
	if src == nil {
		return nil, nil
	}
	if src.Ref == "" {
		if src.Value == nil {
			return nil, nil
		}
		v, err := p.callback(src.Value, cur)
		if err != nil {
			return nil, err
		}
		return &CallbackRef{Value: v, Summary: src.Summary, Description: src.Description, Extensions: maps.Clone(src.Extensions)}, nil
	}
	if src.Value == nil {
		return nil, p.errUnresolved(src.Ref)
	}
	u, err := p.resolveRef(src.Ref, cur)
	if err != nil {
		return nil, err
	}
	name, err := p.ensureCallbackComponent(u, src.Value)
	if err != nil {
		return nil, err
	}
	dst := &CallbackRef{
		Ref:         "#/components/callbacks/" + escapeJSONPointerToken(name),
		Summary:     src.Summary,
		Description: src.Description,
		Extensions:  maps.Clone(src.Extensions),
	}
	if slot := p.components().Callbacks[name]; slot.Value != nil {
		dst.Value = slot.Value
	} else {
		p.fixups = append(p.fixups, func() { dst.Value = slot.Value })
	}
	return dst, nil
}

func (p *pruner) ensureParameterComponent(u *url.URL, value *Parameter) (string, error) {
	name, key, isNew := p.componentName("parameters", u, "")
	if !isNew {
		return name, nil
	}
	slot := &ParameterRef{}
	p.componentParameters()[name] = slot
	p.pushChain(fmt.Sprintf("#/components/parameters/%s (from %s)", name, key))
	body, err := p.parameter(value, u)
	p.popChain()
	if err != nil {
		return "", err
	}
	slot.Value = body
	return name, nil
}

func (p *pruner) ensureHeaderComponent(u *url.URL, value *Header) (string, error) {
	name, key, isNew := p.componentName("headers", u, "")
	if !isNew {
		return name, nil
	}
	slot := &HeaderRef{}
	p.componentHeaders()[name] = slot
	p.pushChain(fmt.Sprintf("#/components/headers/%s (from %s)", name, key))
	body, err := p.header(value, u)
	p.popChain()
	if err != nil {
		return "", err
	}
	slot.Value = body
	return name, nil
}

func (p *pruner) ensureRequestBodyComponent(u *url.URL, value *RequestBody) (string, error) {
	name, key, isNew := p.componentName("requestBodies", u, "")
	if !isNew {
		return name, nil
	}
	slot := &RequestBodyRef{}
	p.componentRequestBodies()[name] = slot
	p.pushChain(fmt.Sprintf("#/components/requestBodies/%s (from %s)", name, key))
	body, err := p.requestBody(value, u)
	p.popChain()
	if err != nil {
		return "", err
	}
	slot.Value = body
	return name, nil
}

func (p *pruner) ensureResponseComponent(u *url.URL, value *Response) (string, error) {
	name, key, isNew := p.componentName("responses", u, "")
	if !isNew {
		return name, nil
	}
	slot := &ResponseRef{}
	p.componentResponses()[name] = slot
	p.pushChain(fmt.Sprintf("#/components/responses/%s (from %s)", name, key))
	body, err := p.response(value, u)
	p.popChain()
	if err != nil {
		return "", err
	}
	slot.Value = body
	return name, nil
}

func (p *pruner) ensureSecuritySchemeComponent(u *url.URL, value *SecurityScheme, nameHint string) string {
	name, key, isNew := p.componentName("securitySchemes", u, nameHint)
	if !isNew {
		return name
	}
	slot := &SecuritySchemeRef{}
	p.componentSecuritySchemes()[name] = slot
	p.pushChain(fmt.Sprintf("#/components/securitySchemes/%s (from %s)", name, key))
	slot.Value = cloneSecurityScheme(value)
	p.popChain()
	return name
}

func (p *pruner) ensureExampleComponent(u *url.URL, value *Example) string {
	name, _, isNew := p.componentName("examples", u, "")
	if !isNew {
		return name
	}
	p.componentExamples()[name] = &ExampleRef{Value: cloneExample(value)}
	return name
}

func (p *pruner) ensureLinkComponent(u *url.URL, value *Link) string {
	name, _, isNew := p.componentName("links", u, "")
	if !isNew {
		return name
	}
	p.componentLinks()[name] = &LinkRef{Value: cloneLink(value)}
	return name
}

func (p *pruner) ensureCallbackComponent(u *url.URL, value *Callback) (string, error) {
	name, key, isNew := p.componentName("callbacks", u, "")
	if !isNew {
		return name, nil
	}
	slot := &CallbackRef{}
	p.componentCallbacks()[name] = slot
	p.pushChain(fmt.Sprintf("#/components/callbacks/%s (from %s)", name, key))
	body, err := p.callback(value, u)
	p.popChain()
	if err != nil {
		return "", err
	}
	slot.Value = body
	return name, nil
}

// copyPathItem copies a path item, keeping the operations keep() accepts and
// inlining any path-item-level $ref. It returns the copy and the number of
// operations kept. Path-level parameters and servers are preserved.
func (p *pruner) copyPathItem(src *PathItem, cur *url.URL, where string, keep func(*Operation) bool) (*PathItem, int, error) {
	if src.Ref != "" {
		// The loader inlined the referenced path item; its contents resolve
		// against the referenced document, so rebase before copying.
		u, err := p.resolveRef(src.Ref, cur)
		if err != nil {
			return nil, 0, err
		}
		cur = u
	}
	dst := &PathItem{
		Summary:     src.Summary,
		Description: src.Description,
		Extensions:  maps.Clone(src.Extensions),
		Servers:     cloneServers(src.Servers),
	}
	parameters, err := p.parameters(src.Parameters, cur)
	if err != nil {
		return nil, 0, err
	}
	dst.Parameters = parameters
	kept := 0
	for _, entry := range src.operationEntries() {
		if !keep(entry.operation) {
			continue
		}
		p.pushChain(where + entry.pointerSuffix)
		op, err := p.operation(entry.operation, cur)
		p.popChain()
		if err != nil {
			return nil, 0, err
		}
		dst.SetOperation(entry.method, op)
		kept++
	}
	return dst, kept, nil
}

func (p *pruner) operation(src *Operation, cur *url.URL) (*Operation, error) {
	if src == nil {
		return nil, nil
	}
	dst := &Operation{
		Tags:         slices.Clone(src.Tags),
		Summary:      src.Summary,
		Description:  src.Description,
		OperationID:  src.OperationID,
		Deprecated:   src.Deprecated,
		Extensions:   maps.Clone(src.Extensions),
		ExternalDocs: cloneExternalDocs(src.ExternalDocs),
	}
	if src.Security != nil {
		security := cloneSecurityRequirements(*src.Security)
		dst.Security = &security
		p.collectSecurityNames(*src.Security)
	}
	if src.Servers != nil {
		servers := cloneServers(*src.Servers)
		dst.Servers = &servers
	}
	for _, tag := range src.Tags {
		p.usedTags[tag] = struct{}{}
	}
	var err error
	if dst.Parameters, err = p.parameters(src.Parameters, cur); err != nil {
		return nil, err
	}
	if dst.RequestBody, err = p.requestBodyRef(src.RequestBody, cur); err != nil {
		return nil, err
	}
	if dst.Responses, err = p.responses(src.Responses, cur); err != nil {
		return nil, err
	}
	if dst.Callbacks, err = p.callbacks(src.Callbacks, cur); err != nil {
		return nil, err
	}
	return dst, nil
}

func (p *pruner) parameters(src Parameters, cur *url.URL) (Parameters, error) {
	if src == nil {
		return nil, nil
	}
	out := make(Parameters, len(src))
	for i, ref := range src {
		v, err := p.parameterRef(ref, cur)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (p *pruner) parameter(src *Parameter, cur *url.URL) (*Parameter, error) {
	if src == nil {
		return nil, nil
	}
	dst := *src
	dst.Extensions = maps.Clone(src.Extensions)
	var err error
	if dst.Schema, err = p.schemaRef(src.Schema, cur); err != nil {
		return nil, err
	}
	if dst.Content, err = p.content(src.Content, cur); err != nil {
		return nil, err
	}
	if dst.Examples, err = p.examples(src.Examples, cur); err != nil {
		return nil, err
	}
	return &dst, nil
}

func (p *pruner) header(src *Header, cur *url.URL) (*Header, error) {
	if src == nil {
		return nil, nil
	}
	parameter, err := p.parameter(&src.Parameter, cur)
	if err != nil {
		return nil, err
	}
	return &Header{Parameter: *parameter}, nil
}

func (p *pruner) headers(src Headers, cur *url.URL) (Headers, error) {
	if len(src) == 0 {
		return nil, nil
	}
	out := make(Headers, len(src))
	for _, name := range componentNames(src) {
		v, err := p.headerRef(src[name], cur)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

func (p *pruner) requestBody(src *RequestBody, cur *url.URL) (*RequestBody, error) {
	if src == nil {
		return nil, nil
	}
	dst := &RequestBody{
		Description: src.Description,
		Required:    src.Required,
		Extensions:  maps.Clone(src.Extensions),
	}
	content, err := p.content(src.Content, cur)
	if err != nil {
		return nil, err
	}
	dst.Content = content
	return dst, nil
}

func (p *pruner) responses(src *Responses, cur *url.URL) (*Responses, error) {
	if src == nil {
		return nil, nil
	}
	dst := NewResponses()
	dst.Extensions = maps.Clone(src.Extensions)
	dst.explicitlyNull = src.explicitlyNull
	for _, name := range componentNames(src.Map()) {
		p.pushChain("responses." + name)
		v, err := p.responseRef(src.Map()[name], cur)
		p.popChain()
		if err != nil {
			return nil, err
		}
		dst.Set(name, v)
	}
	return dst, nil
}

func (p *pruner) response(src *Response, cur *url.URL) (*Response, error) {
	if src == nil {
		return nil, nil
	}
	dst := &Response{
		Description: src.Description,
		Extensions:  maps.Clone(src.Extensions),
	}
	var err error
	if dst.Headers, err = p.headers(src.Headers, cur); err != nil {
		return nil, err
	}
	if dst.Content, err = p.content(src.Content, cur); err != nil {
		return nil, err
	}
	if dst.Links, err = p.links(src.Links, cur); err != nil {
		return nil, err
	}
	return dst, nil
}

func (p *pruner) content(src Content, cur *url.URL) (Content, error) {
	if len(src) == 0 {
		return nil, nil
	}
	out := make(Content, len(src))
	for _, name := range componentNames(src) {
		mediaType := src[name]
		if mediaType == nil {
			continue
		}
		dst := &MediaType{
			Example:    mediaType.Example,
			Extensions: maps.Clone(mediaType.Extensions),
		}
		var err error
		if dst.Schema, err = p.schemaRef(mediaType.Schema, cur); err != nil {
			return nil, err
		}
		if dst.ItemSchema, err = p.schemaRef(mediaType.ItemSchema, cur); err != nil {
			return nil, err
		}
		if dst.Examples, err = p.examples(mediaType.Examples, cur); err != nil {
			return nil, err
		}
		if dst.Encoding, err = p.encodings(mediaType.Encoding, cur); err != nil {
			return nil, err
		}
		out[name] = dst
	}
	return out, nil
}

func (p *pruner) encodings(src Encodings, cur *url.URL) (Encodings, error) {
	if len(src) == 0 {
		return nil, nil
	}
	out := make(Encodings, len(src))
	for _, name := range componentNames(src) {
		encoding := src[name]
		if encoding == nil {
			continue
		}
		dst := *encoding
		dst.Extensions = maps.Clone(encoding.Extensions)
		headers, err := p.headers(encoding.Headers, cur)
		if err != nil {
			return nil, err
		}
		dst.Headers = headers
		out[name] = &dst
	}
	return out, nil
}

func (p *pruner) examples(src Examples, cur *url.URL) (Examples, error) {
	if len(src) == 0 {
		return nil, nil
	}
	out := make(Examples, len(src))
	for _, name := range componentNames(src) {
		v, err := p.exampleRef(src[name], cur)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

// links copies a Links map, dropping links whose target operation is not part
// of the selection and recording each drop in the report.
func (p *pruner) links(src Links, cur *url.URL) (Links, error) {
	if len(src) == 0 {
		return nil, nil
	}
	out := make(Links, len(src))
	for _, name := range componentNames(src) {
		linkRef := src[name]
		if linkRef == nil {
			continue
		}
		p.pushChain("links." + name)
		kept, err := p.keepLink(name, linkRef, cur)
		if err != nil {
			p.popChain()
			return nil, err
		}
		if !kept {
			p.popChain()
			continue
		}
		nl, err := p.linkRef(linkRef, cur)
		p.popChain()
		if err != nil {
			return nil, err
		}
		out[name] = nl
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// keepLink reports whether the link's target operation is part of the
// selection. Links are never a reason to keep an operation: a link pointing
// outside the selection is dropped and reported.
func (p *pruner) keepLink(name string, linkRef *LinkRef, cur *url.URL) (bool, error) {
	link := linkRef.Value
	if link == nil {
		if linkRef.Ref == "" {
			return false, nil
		}
		return false, p.errUnresolved(linkRef.Ref)
	}
	linkCur := cur
	if linkRef.Ref != "" {
		// The link object lives in the referenced document; its operationRef
		// resolves against that document, not against the referring one.
		u, err := p.resolveRef(linkRef.Ref, cur)
		if err != nil {
			return false, err
		}
		linkCur = u
	}
	if p.linkTargetKept(link, linkCur) {
		return true, nil
	}
	p.report.RemovedLinks = append(p.report.RemovedLinks, RemovedLink{
		Source:       p.chainString(),
		Name:         name,
		OperationID:  link.OperationID,
		OperationRef: link.OperationRef,
	})
	return false, nil
}

func (p *pruner) linkTargetKept(link *Link, cur *url.URL) bool {
	if id := link.OperationID; id != "" {
		_, ok := p.selected[id]
		return ok
	}
	if ref := link.OperationRef; ref != "" {
		u, err := resolveRefString(p.loader, ref, cur)
		if err != nil {
			return false
		}
		_, ok := p.selectedOp[canonicalRefKey(u)]
		return ok
	}
	// A link without an explicit target binds to the operation it sits in.
	return true
}

func (p *pruner) callbacks(src Callbacks, cur *url.URL) (Callbacks, error) {
	if len(src) == 0 {
		return nil, nil
	}
	out := make(Callbacks, len(src))
	for _, name := range componentNames(src) {
		v, err := p.callbackRef(src[name], cur)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

func (p *pruner) callback(src *Callback, cur *url.URL) (*Callback, error) {
	if src == nil {
		return nil, nil
	}
	dst := NewCallback()
	dst.Extensions = maps.Clone(src.Extensions)
	for _, name := range componentNames(src.Map()) {
		// Callback path items describe requests the API consumer will
		// receive; they are kept whole, never filtered by operationId.
		np, _, err := p.copyPathItem(src.Map()[name], cur, "callbacks."+name, keepAllOperations)
		if err != nil {
			return nil, err
		}
		dst.Set(name, np)
	}
	return dst, nil
}

func (p *pruner) schemaRefs(src SchemaRefs, cur *url.URL) (SchemaRefs, error) {
	if src == nil {
		return nil, nil
	}
	out := make(SchemaRefs, len(src))
	for i, ref := range src {
		v, err := p.schemaRef(ref, cur)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (p *pruner) schemaMap(src Schemas, cur *url.URL) (Schemas, error) {
	if src == nil {
		return nil, nil
	}
	out := make(Schemas, len(src))
	for _, name := range componentNames(src) {
		v, err := p.schemaRef(src[name], cur)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

func (p *pruner) boolSchema(src BoolSchema, cur *url.URL) (BoolSchema, error) {
	out := src
	if src.Schema != nil {
		v, err := p.schemaRef(src.Schema, cur)
		if err != nil {
			return out, err
		}
		out.Schema = v
	}
	return out, nil
}

func (p *pruner) schema(src *Schema, cur *url.URL) (*Schema, error) {
	if src == nil {
		return nil, nil
	}
	dst := *src
	dst.Extensions = maps.Clone(src.Extensions)
	dst.Required = slices.Clone(src.Required)
	dst.Enum = slices.Clone(src.Enum)
	dst.Examples = slices.Clone(src.Examples)
	if src.Type != nil {
		t := slices.Clone(*src.Type)
		dst.Type = &t
	}
	if src.DependentRequired != nil {
		dr := make(map[string][]string, len(src.DependentRequired))
		for name, deps := range src.DependentRequired {
			dr[name] = slices.Clone(deps)
		}
		dst.DependentRequired = dr
	}
	dst.ExternalDocs = cloneExternalDocs(src.ExternalDocs)

	var err error
	if dst.OneOf, err = p.schemaRefs(src.OneOf, cur); err != nil {
		return nil, err
	}
	if dst.AnyOf, err = p.schemaRefs(src.AnyOf, cur); err != nil {
		return nil, err
	}
	if dst.AllOf, err = p.schemaRefs(src.AllOf, cur); err != nil {
		return nil, err
	}
	if dst.PrefixItems, err = p.schemaRefs(src.PrefixItems, cur); err != nil {
		return nil, err
	}
	if dst.Not, err = p.schemaRef(src.Not, cur); err != nil {
		return nil, err
	}
	if dst.Items, err = p.schemaRef(src.Items, cur); err != nil {
		return nil, err
	}
	if dst.Contains, err = p.schemaRef(src.Contains, cur); err != nil {
		return nil, err
	}
	if dst.PropertyNames, err = p.schemaRef(src.PropertyNames, cur); err != nil {
		return nil, err
	}
	if dst.If, err = p.schemaRef(src.If, cur); err != nil {
		return nil, err
	}
	if dst.Then, err = p.schemaRef(src.Then, cur); err != nil {
		return nil, err
	}
	if dst.Else, err = p.schemaRef(src.Else, cur); err != nil {
		return nil, err
	}
	if dst.ContentSchema, err = p.schemaRef(src.ContentSchema, cur); err != nil {
		return nil, err
	}
	if dst.Properties, err = p.schemaMap(src.Properties, cur); err != nil {
		return nil, err
	}
	if dst.PatternProperties, err = p.schemaMap(src.PatternProperties, cur); err != nil {
		return nil, err
	}
	if dst.DependentSchemas, err = p.schemaMap(src.DependentSchemas, cur); err != nil {
		return nil, err
	}
	if dst.Defs, err = p.schemaMap(src.Defs, cur); err != nil {
		return nil, err
	}
	if dst.AdditionalProperties, err = p.boolSchema(src.AdditionalProperties, cur); err != nil {
		return nil, err
	}
	if dst.UnevaluatedItems, err = p.boolSchema(src.UnevaluatedItems, cur); err != nil {
		return nil, err
	}
	if dst.UnevaluatedProperties, err = p.boolSchema(src.UnevaluatedProperties, cur); err != nil {
		return nil, err
	}
	if src.Discriminator != nil {
		if dst.Discriminator, err = p.discriminator(src.Discriminator, cur); err != nil {
			return nil, err
		}
	}
	return &dst, nil
}

func (p *pruner) discriminator(src *Discriminator, cur *url.URL) (*Discriminator, error) {
	dst := &Discriminator{
		PropertyName: src.PropertyName,
		Extensions:   maps.Clone(src.Extensions),
	}
	if len(src.Mapping) > 0 {
		dst.Mapping = make(map[string]MappingRef, len(src.Mapping))
		for _, name := range componentNames(src.Mapping) {
			v, err := p.mappingRef(src.Mapping[name], cur)
			if err != nil {
				return nil, err
			}
			dst.Mapping[name] = v
		}
	}
	return dst, nil
}

// mappingRef rewrites a discriminator mapping value. Mapping values are plain
// strings: either a reference, or a bare schema name shorthand for
// "#/components/schemas/<name>" in the document holding the mapping.
func (p *pruner) mappingRef(src MappingRef, cur *url.URL) (MappingRef, error) {
	ref := src.Ref
	if ref == "" {
		return src, nil
	}
	if strings.Contains(ref, "/") || strings.HasPrefix(ref, "#") {
		schemaRef := SchemaRef(src)
		out, err := p.schemaRef(&schemaRef, cur)
		if err != nil {
			return src, err
		}
		return MappingRef(*out), nil
	}
	if doc := p.sourceDoc(cur); doc != nil && doc.Components != nil {
		if target := doc.Components.Schemas[ref]; target != nil {
			switch {
			case target.Ref != "":
				schemaRef := SchemaRef{Ref: target.Ref, Value: target.Value}
				out, err := p.schemaRef(&schemaRef, cur)
				if err != nil {
					return src, err
				}
				return MappingRef(*out), nil
			case target.Value != nil:
				u := copyURI(cur)
				if u == nil {
					u = new(url.URL)
				}
				u.Fragment = "/components/schemas/" + escapeJSONPointerToken(ref)
				name, err := p.ensureSchemaComponent(u, target.Value)
				if err != nil {
					return src, err
				}
				return MappingRef{Ref: "#/components/schemas/" + escapeJSONPointerToken(name)}, nil
			}
		}
	}
	// Not a reference, or pointing at nothing the loader found: keep as written.
	return src, nil
}

func (p *pruner) collectSecurityNames(srs SecurityRequirements) {
	for _, sr := range srs {
		for name := range sr {
			p.schemeNames[name] = struct{}{}
		}
	}
}

// registerSecuritySchemes copies the security schemes the kept security
// requirements reference and rewrites requirement keys to the names the
// schemes received in the pruned document.
func (p *pruner) registerSecuritySchemes() error {
	if len(p.schemeNames) == 0 {
		return nil
	}
	renames := make(map[string]string, len(p.schemeNames))
	for _, name := range componentNames(p.schemeNames) {
		var ref *SecuritySchemeRef
		if p.doc.Components != nil {
			ref = p.doc.Components.SecuritySchemes[name]
		}
		if ref == nil {
			// Dangling in the source document too; leave the requirement as
			// written and let validation report it.
			continue
		}
		p.pushChain("components.securitySchemes." + name)
		newName, err := p.registerSecurityScheme(name, ref)
		p.popChain()
		if err != nil {
			return err
		}
		renames[name] = newName
	}
	if len(renames) == 0 {
		return nil
	}
	rewriteSecurityRequirements(p.out.Security, renames)
	rewriteOpSecurity := func(pathItem *PathItem) {
		if pathItem == nil {
			return
		}
		for _, op := range pathItem.Operations() {
			if op != nil && op.Security != nil {
				rewriteSecurityRequirements(*op.Security, renames)
			}
		}
	}
	for _, pathItem := range p.out.Paths.Map() {
		rewriteOpSecurity(pathItem)
	}
	for _, pathItem := range p.out.Webhooks {
		rewriteOpSecurity(pathItem)
	}
	return nil
}

func (p *pruner) registerSecurityScheme(name string, ref *SecuritySchemeRef) (string, error) {
	if ref.Ref != "" {
		if ref.Value == nil {
			return "", p.errUnresolved(ref.Ref)
		}
		u, err := p.resolveRef(ref.Ref, p.doc.url)
		if err != nil {
			return "", err
		}
		return p.ensureSecuritySchemeComponent(u, ref.Value, name), nil
	}
	if ref.Value == nil {
		return name, nil
	}
	u := copyURI(p.doc.url)
	if u == nil {
		u = new(url.URL)
	}
	u.Fragment = "/components/securitySchemes/" + escapeJSONPointerToken(name)
	return p.ensureSecuritySchemeComponent(u, ref.Value, name), nil
}

func rewriteSecurityRequirements(srs SecurityRequirements, renames map[string]string) {
	for i, sr := range srs {
		changed := false
		for oldName := range sr {
			if newName, ok := renames[oldName]; ok && newName != oldName {
				changed = true
				break
			}
		}
		if !changed {
			continue
		}
		nsr := make(SecurityRequirement, len(sr))
		for oldName, scopes := range sr {
			if newName, ok := renames[oldName]; ok {
				nsr[newName] = scopes
			} else {
				nsr[oldName] = scopes
			}
		}
		srs[i] = nsr
	}
}

func cloneExternalDocs(src *ExternalDocs) *ExternalDocs {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Extensions = maps.Clone(src.Extensions)
	return &dst
}

func cloneInfo(src *Info) *Info {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Extensions = maps.Clone(src.Extensions)
	if src.Contact != nil {
		contact := *src.Contact
		contact.Extensions = maps.Clone(src.Contact.Extensions)
		dst.Contact = &contact
	}
	if src.License != nil {
		license := *src.License
		license.Extensions = maps.Clone(src.License.Extensions)
		dst.License = &license
	}
	return &dst
}

func cloneServers(src Servers) Servers {
	if src == nil {
		return nil
	}
	out := make(Servers, len(src))
	for i, server := range src {
		out[i] = cloneServer(server)
	}
	return out
}

func cloneServer(src *Server) *Server {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Extensions = maps.Clone(src.Extensions)
	if src.Variables != nil {
		variables := make(ServerVariables, len(src.Variables))
		for name, variable := range src.Variables {
			if variable == nil {
				continue
			}
			v := *variable
			v.Extensions = maps.Clone(variable.Extensions)
			v.Enum = slices.Clone(variable.Enum)
			variables[name] = &v
		}
		dst.Variables = variables
	}
	return &dst
}

func cloneSecurityRequirements(src SecurityRequirements) SecurityRequirements {
	if src == nil {
		return nil
	}
	out := make(SecurityRequirements, len(src))
	for i, sr := range src {
		nsr := make(SecurityRequirement, len(sr))
		for name, scopes := range sr {
			nsr[name] = slices.Clone(scopes)
		}
		out[i] = nsr
	}
	return out
}

func cloneSecurityScheme(src *SecurityScheme) *SecurityScheme {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Extensions = maps.Clone(src.Extensions)
	if flows := src.Flows; flows != nil {
		nf := *flows
		nf.Extensions = maps.Clone(flows.Extensions)
		nf.Implicit = cloneOAuthFlow(flows.Implicit)
		nf.Password = cloneOAuthFlow(flows.Password)
		nf.ClientCredentials = cloneOAuthFlow(flows.ClientCredentials)
		nf.AuthorizationCode = cloneOAuthFlow(flows.AuthorizationCode)
		dst.Flows = &nf
	}
	return &dst
}

func cloneOAuthFlow(src *OAuthFlow) *OAuthFlow {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Extensions = maps.Clone(src.Extensions)
	dst.Scopes = maps.Clone(src.Scopes)
	return &dst
}

func cloneExample(src *Example) *Example {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Extensions = maps.Clone(src.Extensions)
	return &dst
}

func cloneLink(src *Link) *Link {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Extensions = maps.Clone(src.Extensions)
	dst.Parameters = maps.Clone(src.Parameters)
	dst.Server = cloneServer(src.Server)
	return &dst
}
