package openapi3

import (
	"maps"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
)

func (p *pruner) traverseTarget(target *pruneTarget) error {
	refURL, _ := url.Parse(target.key[strings.IndexByte(target.key, ':')+1:])
	source := refURL
	if source != nil {
		source.Fragment = ""
		if source.String() == "" {
			source = nil
		}
	}
	pointer := refURL.Fragment
	switch target.kind {
	case pruneSchemas:
		return p.traverseSchema(target.schema, source, pointer, nil)
	case pruneParameters:
		return p.traverseParameter(target.param, source, pointer, nil)
	case pruneHeaders:
		if target.header != nil {
			return p.traverseParameter(&target.header.Parameter, source, pointer, nil)
		}
	case pruneRequestBodies:
		if target.request != nil {
			return p.traverseContent(target.request.Content, source, pointer+"/content", nil)
		}
	case pruneResponses:
		return p.traverseResponse(target.response, source, pointer, nil)
	case pruneSecuritySchemes:
		// Security scheme objects do not contain component references.
		return nil
	case pruneExamples:
		return nil
	case pruneLinks:
		return nil
	case pruneCallbacks:
		if target.callback != nil {
			return p.traverseCallback(target.callback, source, "", nil, nil)
		}
	}
	return nil
}

func componentNameFromPointer(pointer string) string {
	_, name := path.Split(pointer)
	return unescapeRefString(name)
}

func (p *pruner) evaluateLink(link *Link, source *url.URL, pointer string, chain []string) (bool, error) {
	if link == nil {
		return false, nil
	}
	if link.OperationID != "" {
		return p.byID[link.OperationID] != nil && p.included[p.byID[link.OperationID].op] != nil, nil
	}
	if link.OperationRef == "" {
		return false, nil
	}
	entry, err := p.resolveOperationRef(link.OperationRef, source, chain)
	if err != nil {
		return false, err
	}
	return entry != nil && p.included[entry.op] != nil, nil
}

func (p *pruner) resolveOperationRef(ref string, source *url.URL, chain []string) (*pruneOperation, error) {
	parsed, err := p.loader.resolvePathWithRef(ref, source)
	if err != nil {
		return nil, p.unresolved(ref, source, "", chain)
	}
	fragment := parsed.Fragment
	fileURL := *parsed
	fileURL.Fragment = ""
	if source != nil && parsed.Path == "" {
		fileURL.Scheme, fileURL.Host, fileURL.Path = source.Scheme, source.Host, source.Path
	}

	doc := p.documentAt(&fileURL)
	if doc == nil && samePruneURL(&fileURL, source) {
		doc = p.documentAt(source)
	}
	if doc == nil {
		loaded, loadErr := p.loader.loadFromURIInternal(&fileURL)
		if loadErr != nil {
			return nil, p.unresolved(ref, source, parsed.String(), chain)
		}
		doc = loaded
	}

	value, err := p.pointerInDocument(doc, &fileURL, fragment)
	if err != nil {
		return nil, p.unresolved(ref, &fileURL, fragment, chain)
	}
	op, ok := value.(*Operation)
	if !ok || op == nil {
		return nil, p.unresolved(ref, &fileURL, fragment, chain)
	}
	if entry := p.included[op]; entry != nil {
		return entry, nil
	}
	return nil, nil
}

func (p *pruner) assignNames() error {
	targets := slices.Collect(maps.Values(p.targets))
	slices.SortFunc(targets, func(a, b *pruneTarget) int { return strings.Compare(a.key, b.key) })
	used := map[string]struct{}{}
	for _, target := range targets {
		if !target.root {
			continue
		}
		name := componentNameFromPointer(refURLFromKey(target.key).Fragment)
		if name == "" {
			return &PruneUnresolvedRefError{Ref: target.key, Source: "component name"}
		}
		target.name = name
		used[name] = struct{}{}
	}
	for _, target := range targets {
		if target.root {
			continue
		}
		name := stableComponentName(target)
		base := name
		for i := 1; ; i++ {
			if _, exists := used[name]; !exists {
				break
			}
			name = base + "_" + strconv.Itoa(i)
		}
		target.name = name
		used[name] = struct{}{}
	}
	return nil
}

func refURLFromKey(key string) *url.URL {
	ref := key[strings.IndexByte(key, ':')+1:]
	u, _ := url.Parse(ref)
	return u
}

func stableComponentName(target *pruneTarget) string {
	u := refURLFromKey(target.key)
	fragment := u.Fragment
	if strings.HasPrefix(fragment, "#") {
		fragment = strings.TrimPrefix(fragment, "#")
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" || base == "" {
		base = "root"
	}
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	tokens := strings.Split(fragment, "/")
	for _, token := range tokens {
		if token == "" || token == "components" || token == string(target.kind) {
			continue
		}
		decoded := unescapeRefString(token)
		if decoded != "" {
			base += "_" + decoded
		}
	}
	return InvalidIdentifierCharRegExp.ReplaceAllString(base, "_")
}
