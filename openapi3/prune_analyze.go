package openapi3

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

func (p *pruner) analyze(ctx context.Context) error {
	entries := slices.Collect(maps.Values(p.selected))
	slices.SortFunc(entries, func(a, b *pruneOperation) int { return strings.Compare(a.outputPtr, b.outputPtr) })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, _, source, err := p.resolvePathItem(entry.pathItem(), entry.source, nil)
		if err != nil {
			return err
		}
		if err := p.indexPathItemCallbacks(item, source, nil); err != nil {
			return err
		}
		if entry.op.Security == nil {
			entryDoc := p.documentAt(entry.source)
			if entryDoc != nil {
				for i, requirement := range entryDoc.Security {
					if err := p.securityRequirement(requirement, entry.source, "/security/"+strconv.Itoa(i), nil); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, _, source, err := p.resolvePathItem(entry.pathItem(), entry.source, nil)
		if err != nil {
			return err
		}
		if err := p.traversePathItem(item, source, entry.basePointer(), true, nil); err != nil {
			return err
		}
	}

	for len(p.queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := p.queue[0]
		p.queue = p.queue[1:]
		if err := p.traverseTarget(target); err != nil {
			return err
		}
	}
	return nil
}

func (entry *pruneOperation) pathItem() *PathItem {
	if entry == nil {
		return nil
	}
	if entry.webhook != "" && entry.doc != nil {
		return entry.doc.Webhooks[entry.webhook]
	}
	if entry.doc != nil && entry.doc.Paths != nil {
		return entry.doc.Paths.Value(entry.path)
	}
	return nil
}

func (entry *pruneOperation) basePointer() string {
	if entry != nil && entry.webhook != "" {
		return "/webhooks/" + escapeRefString(entry.webhook)
	}
	return "/paths/" + escapeRefString(entry.path)
}

func (p *pruner) addTarget(kind pruneKind, value any, refURL *url.URL, root bool, chain ...string) *pruneTarget {
	key := targetKey(kind, refURL)
	if target := p.targets[key]; target != nil {
		return target
	}
	target := &pruneTarget{kind: kind, key: key, root: root, uri: fileURI(refURL)}
	if refURL != nil {
		target.uri = fileURI(refURL)
	}
	switch kind {
	case pruneSchemas:
		target.schema, _ = value.(*Schema)
	case pruneParameters:
		target.param, _ = value.(*Parameter)
	case pruneHeaders:
		target.header, _ = value.(*Header)
	case pruneRequestBodies:
		target.request, _ = value.(*RequestBody)
	case pruneResponses:
		target.response, _ = value.(*Response)
	case pruneSecuritySchemes:
		target.security, _ = value.(*SecurityScheme)
	case pruneExamples:
		target.example, _ = value.(*Example)
	case pruneLinks:
		target.link, _ = value.(*Link)
	case pruneCallbacks:
		target.callback, _ = value.(*Callback)
	}
	p.targets[key] = target
	p.queue = append(p.queue, target)
	return target
}

func targetKey(kind pruneKind, refURL *url.URL) string {
	if refURL == nil {
		return string(kind) + ":"
	}
	normalized := *refURL
	if strings.HasPrefix(normalized.Fragment, "#") {
		normalized.Fragment = strings.TrimPrefix(normalized.Fragment, "#")
	}
	return string(kind) + ":" + normalized.String()
}

func fileURI(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.Fragment = ""
	return c.String()
}

func (p *pruner) registerRef(kind pruneKind, refString string, refPath *url.URL, value any, source *url.URL, pointer string, chain []string, root bool) (*pruneTarget, error) {
	if refString == "" {
		return nil, nil
	}
	resolved := refPath
	if resolved != nil {
		copy := *resolved
		resolved = &copy
	}
	if resolved == nil {
		var err error
		resolved, err = p.loader.resolvePathWithRef(refString, source)
		if err != nil {
			return nil, p.unresolved(refString, source, pointer, chain)
		}
	}
	if resolved.Path == "" && source != nil && strings.HasPrefix(refString, "#") {
		resolved.Scheme, resolved.Host, resolved.Path = source.Scheme, source.Host, source.Path
	}
	if resolved.Path == "" && source != nil && refPath != nil {
		resolved.Scheme, resolved.Host, resolved.Path = source.Scheme, source.Host, source.Path
	}
	if strings.HasPrefix(resolved.Fragment, "#") {
		resolved.Fragment = strings.TrimPrefix(resolved.Fragment, "#")
	}
	if isRootComponentFragment(kind, resolved.Fragment) && samePruneURL(resolved, p.doc.url) {
		root = true
	}
	if strings.HasPrefix(refString, "#") {
		if localValue := p.localComponentValue(kind, source, resolved); localValue != nil {
			value = localValue
		}
	}
	target := p.addTarget(kind, value, resolved, root, chain...)
	return target, nil
}

func (p *pruner) localComponentValue(kind pruneKind, source *url.URL, resolved *url.URL) any {
	doc := p.documentAt(source)
	if doc == nil || doc.Components == nil || resolved == nil || resolved.Fragment == "" {
		return nil
	}
	tokens := strings.Split(resolved.Fragment, "/")
	name := unescapeRefString(tokens[len(tokens)-1])
	switch kind {
	case pruneSchemas:
		if ref := doc.Components.Schemas[name]; ref != nil {
			return ref.Value
		}
	case pruneParameters:
		if ref := doc.Components.Parameters[name]; ref != nil {
			return ref.Value
		}
	case pruneHeaders:
		if ref := doc.Components.Headers[name]; ref != nil {
			return ref.Value
		}
	case pruneRequestBodies:
		if ref := doc.Components.RequestBodies[name]; ref != nil {
			return ref.Value
		}
	case pruneResponses:
		if ref := doc.Components.Responses[name]; ref != nil {
			return ref.Value
		}
	case pruneSecuritySchemes:
		if ref := doc.Components.SecuritySchemes[name]; ref != nil {
			return ref.Value
		}
	case pruneExamples:
		if ref := doc.Components.Examples[name]; ref != nil {
			return ref.Value
		}
	case pruneLinks:
		if ref := doc.Components.Links[name]; ref != nil {
			return ref.Value
		}
	case pruneCallbacks:
		if ref := doc.Components.Callbacks[name]; ref != nil {
			return ref.Value
		}
	}
	return nil
}

func isRootComponentFragment(kind pruneKind, fragment string) bool {
	return fragment == "" || strings.HasPrefix(fragment, "/components/"+string(kind)+"/")
}

func (p *pruner) schemaRef(ref *SchemaRef, source *url.URL, pointer string, chain []string) error {
	if ref == nil {
		return nil
	}
	if ref.Ref != "" {
		if ref.Value == nil {
			return p.unresolved(ref.Ref, source, pointer, chain)
		}
		if _, err := p.registerRef(pruneSchemas, ref.Ref, ref.RefPath(), ref.Value, source, pointer, chain, false); err != nil {
			return err
		}
	} else if ref.Value != nil {
		return p.traverseSchema(ref.Value, source, pointer, chain)
	}
	return nil
}

func (p *pruner) traverseSchema(schema *Schema, source *url.URL, pointer string, chain []string) error {
	if schema == nil {
		return nil
	}
	visit := func(ref *SchemaRef, suffix string) error {
		return p.schemaRef(ref, source, pointer+suffix, chain)
	}
	for i, ref := range schema.AllOf {
		if err := visit(ref, "/allOf/"+strconv.Itoa(i)); err != nil {
			return err
		}
	}
	for i, ref := range schema.AnyOf {
		if err := visit(ref, "/anyOf/"+strconv.Itoa(i)); err != nil {
			return err
		}
	}
	for i, ref := range schema.OneOf {
		if err := visit(ref, "/oneOf/"+strconv.Itoa(i)); err != nil {
			return err
		}
	}
	if err := visit(schema.Not, "/not"); err != nil {
		return err
	}
	if err := visit(schema.Items, "/items"); err != nil {
		return err
	}
	if err := visit(schema.AdditionalProperties.Schema, "/additionalProperties"); err != nil {
		return err
	}
	if err := visit(schema.Contains, "/contains"); err != nil {
		return err
	}
	if err := visit(schema.PropertyNames, "/propertyNames"); err != nil {
		return err
	}
	if err := visit(schema.UnevaluatedItems.Schema, "/unevaluatedItems"); err != nil {
		return err
	}
	if err := visit(schema.UnevaluatedProperties.Schema, "/unevaluatedProperties"); err != nil {
		return err
	}
	if err := visit(schema.If, "/if"); err != nil {
		return err
	}
	if err := visit(schema.Then, "/then"); err != nil {
		return err
	}
	if err := visit(schema.Else, "/else"); err != nil {
		return err
	}
	if err := visit(schema.ContentSchema, "/contentSchema"); err != nil {
		return err
	}
	for i, ref := range schema.PrefixItems {
		if err := visit(ref, "/prefixItems/"+strconv.Itoa(i)); err != nil {
			return err
		}
	}
	for name, ref := range schema.Properties {
		if err := visit(ref, "/properties/"+escapeRefString(name)); err != nil {
			return err
		}
	}
	for name, ref := range schema.PatternProperties {
		if err := visit(ref, "/patternProperties/"+escapeRefString(name)); err != nil {
			return err
		}
	}
	for name, ref := range schema.DependentSchemas {
		if err := visit(ref, "/dependentSchemas/"+escapeRefString(name)); err != nil {
			return err
		}
	}
	for name, ref := range schema.Defs {
		if err := visit(ref, "/$defs/"+escapeRefString(name)); err != nil {
			return err
		}
	}
	if schema.Discriminator != nil {
		for name := range schema.Discriminator.Mapping {
			mapping := schema.Discriminator.Mapping[name]
			sourceDoc := p.documentAt(source)
			mappingName := mapping.Ref
			if !strings.Contains(mappingName, "/") {
				if sourceDoc != nil && sourceDoc.Components != nil {
					if sourceRef := sourceDoc.Components.Schemas[mappingName]; sourceRef != nil {
						mapping.Value = sourceRef.Value
					}
				}
				if mapping.Value == nil {
					return p.unresolved("#/components/schemas/"+escapeRefString(mappingName), source, pointer+"/discriminator/mapping/"+escapeRefString(name), chain)
				}
				refPath := (*SchemaRef)(&mapping).RefPath()
				refPath = &url.URL{Fragment: "/components/schemas/" + escapeRefString(mapping.Ref)}
				if source != nil {
					refPath.Scheme, refPath.Host, refPath.Path = source.Scheme, source.Host, source.Path
				}
				ref := "#/components/schemas/" + escapeRefString(mappingName)
				root := samePruneURL(refPath, p.doc.url)
				if _, err := p.registerRef(pruneSchemas, ref, refPath, mapping.Value, source, pointer+"/discriminator/mapping/"+escapeRefString(name), chain, root); err != nil {
					return err
				}
			} else {
				if mapping.Value == nil {
					return p.unresolved(mapping.Ref, source, pointer+"/discriminator/mapping/"+escapeRefString(name), chain)
				}
				if _, err := p.registerRef(pruneSchemas, mapping.Ref, (*SchemaRef)(&mapping).RefPath(), mapping.Value, source, pointer+"/discriminator/mapping/"+escapeRefString(name), chain, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
