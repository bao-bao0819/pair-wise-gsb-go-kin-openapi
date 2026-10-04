package openapi3

import (
	"maps"
	"net/url"
)

func (c *pruneCloner) linkRef(ref *LinkRef, source *url.URL) (*LinkRef, error) {
	if ref == nil {
		return nil, nil
	}
	if ref.Ref != "" {
		target := c.p.targets[targetKey(pruneLinks, c.canonicalRefPath(ref.RefPath()))]
		if target == nil {
			for _, candidate := range c.p.targets {
				if candidate.kind == pruneLinks && candidate.link == ref.Value {
					target = candidate
					break
				}
			}
		}
		if target == nil {
			return nil, nil
		}
		source := refURLFromKey(target.key)
		if source != nil {
			source.Fragment = ""
			if source.String() == "" {
				source = nil
			}
		}
		cloned, err := c.link(target.link, source, nil)
		if err != nil || cloned == nil {
			return nil, err
		}
		return &LinkRef{Ref: c.componentRef(pruneLinks, target.name), Extensions: maps.Clone(ref.Extensions)}, nil
	}
	link, err := c.link(ref.Value, source, nil)
	if err != nil || link == nil {
		return nil, err
	}
	return &LinkRef{Value: link, Extensions: maps.Clone(ref.Extensions)}, nil
}

func (c *pruneCloner) link(value *Link, source *url.URL, _ any) (*Link, error) {
	if value == nil {
		return nil, nil
	}
	if source == nil {
		source = &url.URL{}
	}
	out := &Link{
		Extensions:  maps.Clone(value.Extensions),
		Description: value.Description,
		Parameters:  maps.Clone(value.Parameters),
		Server:      c.server(value.Server),
		RequestBody: value.RequestBody,
	}
	if value.OperationID != "" {
		entry := c.p.byID[value.OperationID]
		if entry != nil && c.p.included[entry.op] != nil {
			out.OperationID = value.OperationID
			return out, nil
		}
		return nil, nil
	}
	entry, err := c.p.resolveOperationRef(value.OperationRef, source, nil)
	if err != nil || entry == nil || c.p.included[entry.op] == nil || entry.outputPtr == "" {
		return nil, nil
	}
	out.OperationRef = entry.outputPtr
	return out, nil
}

func (c *pruneCloner) callback(value *Callback, source *url.URL) (*Callback, error) {
	if value == nil {
		return nil, nil
	}
	out := NewCallbackWithCapacity(value.Len())
	for _, expression := range value.Keys() {
		previous := c.source
		c.source = source
		item, err := c.pathItem(value.Value(expression), nil)
		c.source = previous
		if err != nil {
			return nil, err
		}
		out.Set(expression, item)
	}
	return out, nil
}
