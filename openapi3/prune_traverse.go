package openapi3

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
)

func (p *pruner) traversePathItem(item *PathItem, source *url.URL, pointer string, selectedOnly bool, chain []string) error {
	if item == nil {
		return nil
	}
	pathItemSource := source
	if item.Ref != "" {
		resolved, _, resolvedSource, err := p.resolvePathItem(item, source, chain)
		if err != nil {
			return err
		}
		item, pathItemSource = resolved, resolvedSource
		source = resolvedSource
	}
	for i, parameter := range item.Parameters {
		if err := p.parameterRef(parameter, pathItemSource, pointer+"/parameters/"+strconv.Itoa(i), chain); err != nil {
			return err
		}
	}
	for _, entry := range item.operationEntries() {
		if selectedOnly && p.selected[entry.operation] == nil {
			continue
		}
		if err := p.traverseOperation(entry.operation, pathItemSource, pointer+entry.pointerSuffix, chain); err != nil {
			return err
		}
	}
	return nil
}

func (p *pruner) indexPathItemCallbacks(item *PathItem, source *url.URL, chain []string) error {
	if item == nil {
		return nil
	}
	if item.Ref != "" {
		resolved, _, resolvedSource, err := p.resolvePathItem(item, source, chain)
		if err != nil {
			return err
		}
		item, source = resolved, resolvedSource
	}
	for _, entry := range item.operationEntries() {
		if p.selected[entry.operation] == nil {
			continue
		}
		for _, name := range componentNames(entry.operation.Callbacks) {
			callbackRef := entry.operation.Callbacks[name]
			callback := callbackRef.Value
			callbackSource := source
			if callbackRef != nil && callbackRef.Ref != "" {
				resolved, resolvedSource, err := p.resolveCallback(callbackRef, source, chain)
				if err != nil {
					return err
				}
				callback = resolved
				if refPath := callbackRef.RefPath(); refPath != nil {
					fileURL := *refPath
					fileURL.Fragment = ""
					if fileURL.String() != "" {
						callbackSource = &fileURL
					}
				} else {
					callbackSource = resolvedSource
				}
			}
			if callback != nil {
				if err := p.indexCallbackPaths(callback, callbackSource); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *pruner) traverseOperation(op *Operation, source *url.URL, pointer string, chain []string) error {
	if op == nil {
		return nil
	}
	for i, parameter := range op.Parameters {
		if err := p.parameterRef(parameter, source, pointer+"/parameters/"+strconv.Itoa(i), chain); err != nil {
			return err
		}
	}
	if op.RequestBody != nil {
		if err := p.requestBodyRef(op.RequestBody, source, pointer+"/requestBody", chain); err != nil {
			return err
		}
	}
	if op.Responses != nil {
		for _, code := range op.Responses.Keys() {
			if err := p.responseRef(op.Responses.Value(code), source, pointer+"/responses/"+escapeRefString(code), chain, false); err != nil {
				return err
			}
		}
	}
	for _, name := range componentNames(op.Callbacks) {
		if err := p.callbackRef(op.Callbacks[name], source, pointer+"/callbacks/"+escapeRefString(name), chain, nil); err != nil {
			return err
		}
	}
	if op.Security != nil {
		for i, requirement := range *op.Security {
			if err := p.securityRequirement(requirement, source, pointer+"/security/"+strconv.Itoa(i), chain); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *pruner) parameterRef(ref *ParameterRef, source *url.URL, pointer string, chain []string) error {
	if ref == nil {
		return nil
	}
	if ref.Ref != "" {
		if ref.Value == nil {
			return p.unresolved(ref.Ref, source, pointer, chain)
		}
		if _, err := p.registerRef(pruneParameters, ref.Ref, ref.RefPath(), ref.Value, source, pointer, chain, false); err != nil {
			return err
		}
		return nil
	}
	return p.traverseParameter(ref.Value, source, pointer, chain)
}

func (p *pruner) traverseParameter(parameter *Parameter, source *url.URL, pointer string, chain []string) error {
	if parameter == nil {
		return nil
	}
	if err := p.schemaRef(parameter.Schema, source, pointer+"/schema", chain); err != nil {
		return err
	}
	return p.traverseContent(parameter.Content, source, pointer+"/content", chain)
}

func (p *pruner) headerRef(ref *HeaderRef, source *url.URL, pointer string, chain []string) error {
	if ref == nil {
		return nil
	}
	if ref.Ref != "" {
		if ref.Value == nil {
			return p.unresolved(ref.Ref, source, pointer, chain)
		}
		if _, err := p.registerRef(pruneHeaders, ref.Ref, ref.RefPath(), ref.Value, source, pointer, chain, false); err != nil {
			return err
		}
		return nil
	}
	if ref.Value != nil {
		return p.traverseParameter(&ref.Value.Parameter, source, pointer, chain)
	}
	return nil
}

func (p *pruner) requestBodyRef(ref *RequestBodyRef, source *url.URL, pointer string, chain []string) error {
	if ref == nil {
		return nil
	}
	if ref.Ref != "" {
		if ref.Value == nil {
			return p.unresolved(ref.Ref, source, pointer, chain)
		}
		if _, err := p.registerRef(pruneRequestBodies, ref.Ref, ref.RefPath(), ref.Value, source, pointer, chain, false); err != nil {
			return err
		}
		return nil
	}
	if ref.Value != nil {
		return p.traverseContent(ref.Value.Content, source, pointer+"/content", chain)
	}
	return nil
}

func (p *pruner) responseRef(ref *ResponseRef, source *url.URL, pointer string, chain []string, keepLinks bool) error {
	if ref == nil {
		return nil
	}
	if ref.Ref != "" {
		if ref.Value == nil {
			return p.unresolved(ref.Ref, source, pointer, chain)
		}
		target, err := p.registerRef(pruneResponses, ref.Ref, ref.RefPath(), ref.Value, source, pointer, chain, false)
		if err != nil {
			return err
		}
		_ = target
		return nil
	}
	return p.traverseResponse(ref.Value, source, pointer, chain)
}

func (p *pruner) traverseResponse(response *Response, source *url.URL, pointer string, chain []string) error {
	if response == nil {
		return nil
	}
	if err := p.traverseContent(response.Content, source, pointer+"/content", chain); err != nil {
		return err
	}
	for _, name := range componentNames(response.Headers) {
		if err := p.headerRef(response.Headers[name], source, pointer+"/headers/"+escapeRefString(name), chain); err != nil {
			return err
		}
	}
	for _, name := range componentNames(response.Links) {
		linkRef := response.Links[name]
		linkPointer := pointer + "/links/" + escapeRefString(name)
		retained := false
		var linkValue *Link
		if linkRef != nil {
			linkValue = linkRef.Value
			if linkRef.Ref != "" {
				if linkRef.Value == nil {
					return p.unresolved(linkRef.Ref, source, linkPointer, chain)
				}
				_, err := p.registerRef(pruneLinks, linkRef.Ref, linkRef.RefPath(), linkRef.Value, source, linkPointer, chain, false)
				if err != nil {
					return err
				}
				retained, err = p.evaluateLink(linkRef.Value, source, linkPointer, chain)
				if err != nil {
					return err
				}
			} else if linkRef.Value != nil {
				var err error
				retained, err = p.evaluateLink(linkRef.Value, source, linkPointer, chain)
				if err != nil {
					return err
				}
			}
		}
		if !retained {
			var operationID, operationRef string
			if linkValue != nil {
				operationID, operationRef = linkValue.OperationID, linkValue.OperationRef
			}
			p.addPrunedLink(PrunedLink{
				Name:         name,
				Source:       pruneSourceString(source, linkPointer),
				OperationID:  operationID,
				OperationRef: operationRef,
			})
		}
	}
	return nil
}

func (p *pruner) addPrunedLink(report PrunedLink) {
	if p.prunedSeen == nil {
		p.prunedSeen = map[string]PrunedLink{}
	}
	key := report.Name + "\x00" + report.Source + "\x00" + report.OperationID + "\x00" + report.OperationRef
	if _, exists := p.prunedSeen[key]; exists {
		return
	}
	p.prunedSeen[key] = report
	p.pruned = append(p.pruned, report)
}

func pruneSourceString(source *url.URL, pointer string) string {
	if source == nil {
		return pointer
	}
	file := *source
	file.Fragment = ""
	return file.String() + "#" + strings.TrimPrefix(pointer, "#")
}

func (p *pruner) traverseContent(content Content, source *url.URL, pointer string, chain []string) error {
	for _, name := range componentNames(content) {
		media := content[name]
		mediaPointer := pointer + "/" + escapeRefString(name)
		if err := p.schemaRef(media.Schema, source, mediaPointer+"/schema", chain); err != nil {
			return err
		}
		if err := p.schemaRef(media.ItemSchema, source, mediaPointer+"/itemSchema", chain); err != nil {
			return err
		}
		for _, exampleName := range componentNames(media.Examples) {
			if err := p.exampleRef(media.Examples[exampleName], source, mediaPointer+"/examples/"+escapeRefString(exampleName), chain); err != nil {
				return err
			}
		}
		for _, property := range componentNames(media.Encoding) {
			encoding := media.Encoding[property]
			for _, headerName := range componentNames(encoding.Headers) {
				if err := p.headerRef(encoding.Headers[headerName], source, mediaPointer+"/encoding/"+escapeRefString(property)+"/headers/"+escapeRefString(headerName), chain); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *pruner) exampleRef(ref *ExampleRef, source *url.URL, pointer string, chain []string) error {
	if ref == nil || ref.Ref == "" {
		return nil
	}
	if ref.Value == nil {
		return p.unresolved(ref.Ref, source, pointer, chain)
	}
	_, err := p.registerRef(pruneExamples, ref.Ref, ref.RefPath(), ref.Value, source, pointer, chain, false)
	return err
}

func (p *pruner) callbackRef(ref *CallbackRef, source *url.URL, pointer string, chain []string, callbackPath []string) error {
	if ref == nil {
		return nil
	}
	if ref.Ref != "" {
		if ref.Value == nil {
			return p.unresolved(ref.Ref, source, pointer, chain)
		}
		var err error
		callback, resolvedSource, resolveErr := p.resolveCallback(ref, source, chain)
		if resolveErr != nil {
			return resolveErr
		}
		_, err = p.registerRef(pruneCallbacks, ref.Ref, ref.RefPath(), callback, source, pointer, chain, false)
		if err != nil {
			return err
		}
		source = resolvedSource
	} else if ref.Value != nil {
		if err := p.traverseCallback(ref.Value, source, pointer, chain, callbackPath); err != nil {
			return err
		}
	}
	return nil
}

func (p *pruner) resolveCallback(ref *CallbackRef, source *url.URL, chain []string) (*Callback, *url.URL, error) {
	if ref == nil || ref.Ref == "" {
		return ref.Value, source, nil
	}
	if ref.Value != nil {
		return ref.Value, source, nil
	}
	resolved := ref.RefPath()
	if resolved == nil {
		var err error
		resolved, err = p.loader.resolvePathWithRef(ref.Ref, source)
		if err != nil {
			return nil, nil, p.unresolved(ref.Ref, source, "", chain)
		}
	}
	if resolved.Fragment == "" {
		var loaded Callback
		_, err := p.loader.loadSingleElementFromURI(ref.Ref, source, &loaded)
		if err != nil {
			return nil, nil, p.unresolved(ref.Ref, source, resolved.String(), chain)
		}
		return &loaded, resolved, nil
	}
	value, err := p.pointerInDocument(p.documentAt(source), source, resolved.Fragment)
	if err != nil {
		return nil, nil, p.unresolved(ref.Ref, source, resolved.Fragment, chain)
	}
	callback, _ := value.(*Callback)
	if callback == nil {
		return nil, nil, p.unresolved(ref.Ref, source, resolved.Fragment, chain)
	}
	return callback, source, nil
}

func (p *pruner) traverseCallback(callback *Callback, source *url.URL, pointer string, chain []string, callbackPath []string) error {
	for _, expression := range callback.Keys() {
		item := callback.Value(expression)
		expressionPath := append(append([]string(nil), callbackPath...), expression)
		if err := p.indexCallbackPathItem(source, item, expressionPath); err != nil {
			return err
		}
		if err := p.traversePathItem(item, source, pointer+"/"+escapeRefString(expression), false, chain); err != nil {
			return err
		}
	}
	return nil
}

func (p *pruner) indexCallbackPaths(callback *Callback, source *url.URL) error {
	for _, expression := range callback.Keys() {
		if err := p.indexCallbackPathItem(source, callback.Value(expression), []string{expression}); err != nil {
			return err
		}
	}
	return nil
}

func (p *pruner) indexCallbackPathItem(source *url.URL, item *PathItem, callbackPath []string) error {
	if item == nil {
		return nil
	}
	resolved, doc, resolvedSource, err := p.resolvePathItem(item, source, nil)
	if err != nil {
		return err
	}
	for _, method := range componentNames(resolved.Operations()) {
		op := resolved.Operations()[method]
		if op == nil || op.OperationID == "" {
			continue
		}
		entry := &pruneOperation{
			doc:      doc,
			source:   resolvedSource,
			method:   method,
			op:       op,
			callback: slices.Clone(callbackPath),
		}
		if len(callbackPath) > 0 {
			entry.outputPtr = callbackOutputPointer(callbackPath, method)
		}
		p.included[op] = entry
		p.byID[op.OperationID] = entry
	}
	return nil
}

func callbackOutputPointer(callbackPath []string, method string) string {
	if len(callbackPath) == 0 {
		return ""
	}
	first := callbackPath[0]
	rest := callbackPath[1:]
	var pointer string
	if strings.HasPrefix(first, "https:") || strings.HasPrefix(first, "http:") {
		pointer = ""
		for _, token := range callbackPath {
			pointer += "/" + escapeRefString(token)
		}
	} else if first == "$webhooks" && len(rest) > 0 {
		pointer = "/webhooks/" + escapeRefString(rest[0])
		for _, token := range rest[1:] {
			pointer += "/" + escapeRefString(token)
		}
	} else {
		for _, token := range callbackPath {
			pointer += "/" + escapeRefString(token)
		}
	}
	return pointer + "/" + strings.ToLower(method)
}

func (p *pruner) securityRequirement(requirement SecurityRequirement, source *url.URL, pointer string, chain []string) error {
	doc := p.documentAt(source)
	for _, name := range componentNames(requirement) {
		var ref *SecuritySchemeRef
		if doc != nil && doc.Components != nil {
			ref = doc.Components.SecuritySchemes[name]
		}
		if ref == nil || ref.Value == nil {
			return p.unresolved("#/components/securitySchemes/"+escapeRefString(name), source, pointer, chain)
		}
		if ref.Ref == "" {
			schemeDoc := doc.url
			if schemeDoc == nil {
				schemeDoc = source
			}
			refPath := schemeDoc
			if refPath == nil {
				refPath = &url.URL{}
			}
			copyURL := *refPath
			copyURL.Fragment = "/components/securitySchemes/" + escapeRefString(name)
			p.addTarget(pruneSecuritySchemes, ref.Value, &copyURL, samePruneURL(&copyURL, p.doc.url))
			continue
		}
		refPath := ref.RefPath()
		root := samePruneURL(refPath, p.doc.url)
		if _, err := p.registerRef(pruneSecuritySchemes, ref.Ref, refPath, ref.Value, source, pointer+"/"+escapeRefString(name), chain, root); err != nil {
			return err
		}
	}
	return nil
}
