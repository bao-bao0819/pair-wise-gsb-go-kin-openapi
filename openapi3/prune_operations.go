package openapi3

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// PrunedLink describes a link which was removed because its target operation
// was not selected.
type PrunedLink struct {
	Name         string
	Source       string
	OperationID  string
	OperationRef string
}

// PruneOperationsResult contains a self-contained pruned document.
type PruneOperationsResult struct {
	Document     *T
	PrunedLinks  []PrunedLink
	OperationIDs []string
}

// PruneUnresolvedRefError includes the complete chain of references followed before
// a reference could not be resolved.
type PruneUnresolvedRefError struct {
	Ref    string
	Source string
	Chain  []string
}

func (err *PruneUnresolvedRefError) Error() string {
	if len(err.Chain) == 0 {
		return fmt.Sprintf("unresolved reference %q at %s", err.Ref, err.Source)
	}
	return fmt.Sprintf("unresolved reference %q at %s via %s", err.Ref, err.Source, strings.Join(err.Chain, " -> "))
}

type pruneKind string

const (
	pruneSchemas         pruneKind = "schemas"
	pruneParameters      pruneKind = "parameters"
	pruneHeaders         pruneKind = "headers"
	pruneRequestBodies   pruneKind = "requestBodies"
	pruneResponses       pruneKind = "responses"
	pruneSecuritySchemes pruneKind = "securitySchemes"
	pruneExamples        pruneKind = "examples"
	pruneLinks           pruneKind = "links"
	pruneCallbacks       pruneKind = "callbacks"
)

type pruneTarget struct {
	kind pruneKind
	key  string
	uri  string
	name string
	root bool

	schema   *Schema
	param    *Parameter
	header   *Header
	request  *RequestBody
	response *Response
	security *SecurityScheme
	example  *Example
	link     *Link
	callback *Callback
}

type pruneOperation struct {
	doc       *T
	source    *url.URL
	path      string
	webhook   string
	callback  []string
	method    string
	op        *Operation
	outputPtr string
}

type pruner struct {
	doc    *T
	loader *Loader

	ids        []string
	selected   map[*Operation]*pruneOperation
	included   map[*Operation]*pruneOperation
	byID       map[string]*pruneOperation
	targets    map[string]*pruneTarget
	queue      []*pruneTarget
	pruned     []PrunedLink
	prunedSeen map[string]PrunedLink
}

// PruneOperations creates a new self-contained document containing selected
// operations and all components reachable from them. The source document is not
// modified.
func PruneOperations(ctx context.Context, doc *T, operationIDs []string, loader *Loader) (*PruneOperationsResult, error) {
	_ = ctx
	if doc == nil {
		return nil, fmt.Errorf("document is nil")
	}
	if loader == nil {
		return nil, fmt.Errorf("loader is nil")
	}

	p := &pruner{
		doc:        doc,
		loader:     loader,
		selected:   map[*Operation]*pruneOperation{},
		included:   map[*Operation]*pruneOperation{},
		byID:       map[string]*pruneOperation{},
		targets:    map[string]*pruneTarget{},
		prunedSeen: map[string]PrunedLink{},
	}
	p.ids = append(p.ids, operationIDs...)
	slices.Sort(p.ids)
	p.ids = slices.Compact(p.ids)

	if err := p.indexRootOperations(); err != nil {
		return nil, err
	}
	for _, id := range p.ids {
		entry := p.byID[id]
		if entry == nil {
			return nil, fmt.Errorf("operationId %q was not found", id)
		}
		p.selected[entry.op] = entry
		p.included[entry.op] = entry
	}

	if err := p.analyze(ctx); err != nil {
		return nil, err
	}
	if err := p.assignNames(); err != nil {
		return nil, err
	}

	cloner := newPruneCloner(p)
	out, err := cloner.build()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(p.pruned, func(a, b PrunedLink) int {
		return slices.Compare([]string{a.Name, a.Source}, []string{b.Name, b.Source})
	})
	return &PruneOperationsResult{Document: out, PrunedLinks: slices.Clone(p.pruned), OperationIDs: slices.Clone(p.ids)}, nil
}

func (p *pruner) unresolved(ref string, source *url.URL, pointer string, chain []string) error {
	complete := append(slices.Clone(chain), pointer)
	sourceName := ""
	if source != nil {
		sourceName = source.String()
	}
	return &PruneUnresolvedRefError{Ref: ref, Source: sourceName + pointer, Chain: complete}
}

func (p *pruner) documentAt(source *url.URL) *T {
	if source == nil || samePruneURL(p.doc.url, source) {
		return p.doc
	}
	return p.loader.visitedDocuments[source.String()]
}

func samePruneURL(a, b *url.URL) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
	x.Fragment, y.Fragment = "", ""
	return x.String() == y.String()
}

func (p *pruner) indexRootOperations() error {
	if p.doc.Paths != nil {
		for _, path := range p.doc.Paths.Keys() {
			if err := p.indexPathItem(p.doc, p.doc.url, p.doc.Paths.Value(path), path, "", nil); err != nil {
				return err
			}
		}
	}
	for _, name := range componentNames(p.doc.Webhooks) {
		if err := p.indexPathItem(p.doc, p.doc.url, p.doc.Webhooks[name], "", name, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *pruner) indexPathItem(doc *T, source *url.URL, item *PathItem, path, webhook string, callback []string) error {
	if item == nil {
		return nil
	}
	if item.Ref != "" {
		resolvedItem, resolvedDoc, resolvedSource, err := p.resolvePathItem(item, source, nil)
		if err != nil {
			return err
		}
		item, doc, source = resolvedItem, resolvedDoc, resolvedSource
	}

	base := pruneOperation{doc: doc, source: source, path: path, webhook: webhook, callback: slices.Clone(callback)}
	if webhook != "" {
		base.outputPtr = "/webhooks/" + escapeRefString(webhook)
	} else {
		base.outputPtr = "/paths/" + escapeRefString(path)
	}
	for _, method := range componentNames(item.Operations()) {
		entry := base
		entry.method = method
		entry.op = item.Operations()[method]
		entry.outputPtr += "/" + strings.ToLower(method)
		if entry.op != nil && entry.op.OperationID != "" {
			p.byID[entry.op.OperationID] = &entry
		}
	}
	return nil
}

func (p *pruner) resolvePathItem(item *PathItem, source *url.URL, chain []string) (*PathItem, *T, *url.URL, error) {
	if item == nil || item.Ref == "" {
		return item, p.documentAt(source), source, nil
	}
	var refURL *url.URL
	if refURL == nil {
		var err error
		refURL, err = p.loader.resolvePathWithRef(item.Ref, source)
		if err != nil {
			return nil, nil, nil, p.unresolved(item.Ref, source, "", chain)
		}
	}
	if refURL.Fragment == "" || refURL.Fragment == "/" {
		loaded, err := p.loader.loadFromURIInternal(refURL)
		if err != nil || loaded == nil || loaded.Paths == nil {
			return nil, nil, nil, p.unresolved(item.Ref, source, refURL.String(), chain)
		}
		return loaded.Paths.Value("/"), loaded, refURL, nil
	}

	targetDoc := p.documentAt(source)
	if refURL.Path != "" && (source == nil || refURL.Scheme != source.Scheme || refURL.Host != source.Host || refURL.Path != source.Path) {
		fileURL := *refURL
		fileURL.Fragment = ""
		if loaded := p.documentAt(&fileURL); loaded != nil {
			targetDoc = loaded
			source = &fileURL
		}
	}
	resolved, err := p.pointerInDocument(targetDoc, source, refURL.Fragment)
	if err != nil {
		return nil, nil, nil, p.unresolved(item.Ref, source, refURL.Fragment, chain)
	}
	pathItem, _ := resolved.(*PathItem)
	if pathItem == nil {
		return nil, nil, nil, p.unresolved(item.Ref, source, refURL.Fragment, chain)
	}
	return pathItem, targetDoc, source, nil
}

func (p *pruner) pointerInDocument(doc *T, source *url.URL, fragment string) (any, error) {
	cursor := any(doc)
	if fragment == "" || fragment == "/" {
		return cursor, nil
	}
	if !strings.HasPrefix(fragment, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", fragment)
	}
	for _, rawToken := range strings.Split(fragment, "/")[1:] {
		token := unescapeRefString(rawToken)
		var err error
		cursor, err = p.pruneLookup(cursor, token, source)
		if err != nil {
			return nil, err
		}
	}
	return cursor, nil
}

func (p *pruner) pruneLookup(cursor any, token string, source *url.URL) (any, error) {
	switch value := cursor.(type) {
	case *T:
		switch token {
		case "paths":
			return value.Paths, nil
		case "webhooks":
			return value.Webhooks, nil
		case "components":
			return value.Components, nil
		case "info":
			return value.Info, nil
		}
	case *Paths:
		return value.Value(token), nil
	case Paths:
		return value.Value(token), nil
	case *PathItem:
		if value == nil {
			return nil, fmt.Errorf("path item is nil")
		}
		if op := value.GetOperation(token); op != nil {
			return op, nil
		}
		if op := value.GetOperation(strings.ToUpper(token)); op != nil {
			return op, nil
		}
		if token == "parameters" {
			return value.Parameters, nil
		}
	case PathItem:
		if op := value.GetOperation(token); op != nil {
			return op, nil
		}
		if op := value.GetOperation(strings.ToUpper(token)); op != nil {
			return op, nil
		}
		if token == "parameters" {
			return value.Parameters, nil
		}
	case *Operation:
		switch token {
		case "requestBody":
			return value.RequestBody, nil
		case "responses":
			return value.Responses, nil
		case "callbacks":
			return value.Callbacks, nil
		case "parameters":
			return value.Parameters, nil
		}
	case *Responses:
		return value.Value(token), nil
	case Responses:
		return value.Value(token), nil
	case *Callback:
		return value.Value(token), nil
	case Callback:
		return value.Value(token), nil
	case map[string]*PathItem:
		return value[token], nil
	case Parameters:
		index, err := atoi(token)
		if err != nil || index >= len(value) {
			return nil, fmt.Errorf("invalid parameter index %q", token)
		}
		return value[index], nil
	case *RequestBodyRef:
		return value.Value, nil
	case *ResponseRef:
		return value.Value, nil
	case *CallbackRef:
		return value.Value, nil
	case *ParameterRef:
		return value.Value, nil
	}
	return nil, fmt.Errorf("JSON pointer token %q not found", token)
}

func atoi(value string) (int, error) {
	var n int
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("not an integer")
		}
		n = n*10 + int(char-'0')
	}
	return n, nil
}
