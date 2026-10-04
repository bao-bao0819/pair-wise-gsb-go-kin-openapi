package openapi3

import (
	"maps"
	"net/url"
)

func (c *pruneCloner) canonicalRefPath(refPath *url.URL) *url.URL {
	if refPath == nil {
		return nil
	}
	copyURL := *refPath
	if len(copyURL.Fragment) > 0 && copyURL.Fragment[0] == '#' {
		copyURL.Fragment = copyURL.Fragment[1:]
	}
	return &copyURL
}

func (c *pruneCloner) parameterRef(ref *ParameterRef) (*ParameterRef, error) {
	if ref == nil {
		return nil, nil
	}
	if ref.Ref != "" {
		target := c.targetForValue(pruneParameters, c.canonicalRefPath(ref.RefPath()), ref.Value)
		if target == nil {
			return nil, &PruneUnresolvedRefError{Ref: ref.Ref}
		}
		return &ParameterRef{Ref: c.componentRef(pruneParameters, target.name), Extensions: maps.Clone(ref.Extensions)}, nil
	}
	return &ParameterRef{Value: c.parameter(ref.Value), Extensions: maps.Clone(ref.Extensions)}, nil
}

func (c *pruneCloner) requestBodyRef(ref *RequestBodyRef) (*RequestBodyRef, error) {
	if ref == nil {
		return nil, nil
	}
	if ref.Ref != "" {
		target := c.targetForValue(pruneRequestBodies, c.canonicalRefPath(ref.RefPath()), ref.Value)
		if target == nil {
			return nil, &PruneUnresolvedRefError{Ref: ref.Ref}
		}
		return &RequestBodyRef{Ref: c.componentRef(pruneRequestBodies, target.name), Extensions: maps.Clone(ref.Extensions)}, nil
	}
	return &RequestBodyRef{Value: c.requestBody(ref.Value), Extensions: maps.Clone(ref.Extensions)}, nil
}

func (c *pruneCloner) responseRef(ref *ResponseRef, source *url.URL) (*ResponseRef, error) {
	if ref == nil {
		return nil, nil
	}
	if ref.Ref != "" {
		target := c.targetForValue(pruneResponses, c.canonicalRefPath(ref.RefPath()), ref.Value)
		if target == nil {
			return nil, &PruneUnresolvedRefError{Ref: ref.Ref}
		}
		return &ResponseRef{Ref: c.componentRef(pruneResponses, target.name), Extensions: maps.Clone(ref.Extensions)}, nil
	}
	response, err := c.response(ref.Value, source)
	return &ResponseRef{Value: response, Extensions: maps.Clone(ref.Extensions)}, err
}

func (c *pruneCloner) headerRef(ref *HeaderRef) (*HeaderRef, error) {
	if ref == nil {
		return nil, nil
	}
	if ref.Ref != "" {
		target := c.targetForValue(pruneHeaders, c.canonicalRefPath(ref.RefPath()), ref.Value)
		if target == nil {
			return nil, &PruneUnresolvedRefError{Ref: ref.Ref}
		}
		return &HeaderRef{Ref: c.componentRef(pruneHeaders, target.name), Extensions: maps.Clone(ref.Extensions)}, nil
	}
	return &HeaderRef{Value: c.header(ref.Value), Extensions: maps.Clone(ref.Extensions)}, nil
}

func (c *pruneCloner) exampleRef(ref *ExampleRef) *ExampleRef {
	if ref == nil {
		return nil
	}
	if ref.Ref != "" {
		target := c.targetForValue(pruneExamples, c.canonicalRefPath(ref.RefPath()), ref.Value)
		if target != nil {
			return &ExampleRef{Ref: c.componentRef(pruneExamples, target.name), Extensions: maps.Clone(ref.Extensions)}
		}
	}
	return &ExampleRef{Value: c.example(ref.Value), Extensions: maps.Clone(ref.Extensions)}
}

func (c *pruneCloner) callbackRef(ref *CallbackRef) (*CallbackRef, error) {
	if ref == nil {
		return nil, nil
	}
	if ref.Ref != "" {
		target := c.targetForValue(pruneCallbacks, c.canonicalRefPath(ref.RefPath()), ref.Value)
		if target == nil {
			return nil, &PruneUnresolvedRefError{Ref: ref.Ref}
		}
		return &CallbackRef{Ref: c.componentRef(pruneCallbacks, target.name), Extensions: maps.Clone(ref.Extensions)}, nil
	}
	callback, err := c.callback(ref.Value, nil)
	return &CallbackRef{Value: callback, Extensions: maps.Clone(ref.Extensions)}, err
}

func (c *pruneCloner) targetForValue(kind pruneKind, refPath *url.URL, value any) *pruneTarget {
	target := c.p.targets[targetKey(kind, refPath)]
	if target != nil {
		return target
	}
	for _, candidate := range c.p.targets {
		if candidate.kind != kind {
			continue
		}
		match := false
		switch kind {
		case pruneSchemas:
			match = candidate.schema == value
		case pruneParameters:
			match = candidate.param == value
		case pruneHeaders:
			match = candidate.header == value
		case pruneRequestBodies:
			match = candidate.request == value
		case pruneResponses:
			match = candidate.response == value
		case pruneCallbacks:
			match = candidate.callback == value
		case pruneLinks:
			match = candidate.link == value
		case pruneExamples:
			match = candidate.example == value
		}
		if match {
			return candidate
		}
	}
	return nil
}
