package openapi3

import (
	"maps"
	"net/url"
	"slices"
)

type pruneCloner struct {
	p          *pruner
	schemas    map[*Schema]*Schema
	servers    map[*Server]*Server
	components *Components
	source     *url.URL
}

func newPruneCloner(p *pruner) *pruneCloner {
	return &pruneCloner{p: p, schemas: map[*Schema]*Schema{}, servers: map[*Server]*Server{}}
}

func (c *pruneCloner) build() (*T, error) {
	doc := c.p.doc
	out := &T{
		OpenAPI:           doc.OpenAPI,
		Info:              c.info(doc.Info),
		Servers:           c.serversList(doc.Servers),
		Security:          c.securityRequirements(doc.Security),
		JSONSchemaDialect: doc.JSONSchemaDialect,
	}
	out.Paths = NewPathsWithCapacity(len(c.p.ids))
	out.Webhooks = map[string]*PathItem{}

	c.buildComponentPlaceholders(out)
	if err := c.populateComponents(); err != nil {
		return nil, err
	}

	entries := slices.Collect(maps.Values(c.p.selected))
	slices.SortFunc(entries, func(a, b *pruneOperation) int {
		return slices.Compare([]string{a.op.OperationID, a.outputPtr}, []string{b.op.OperationID, b.outputPtr})
	})
	pathItems := map[string]*PathItem{}
	webhookItems := map[string]*PathItem{}
	for _, entry := range entries {
		item, err := c.selectedPathItem(entry)
		if err != nil {
			return nil, err
		}
		if entry.webhook == "" {
			target := pathItems[entry.path]
			if target == nil {
				target = &PathItem{Summary: item.Summary, Description: item.Description, Servers: item.Servers, Parameters: item.Parameters, Extensions: item.Extensions}
				pathItems[entry.path] = target
			}
			if len(target.Servers) == 0 && entry.op.Servers != nil {
				target.Servers = c.serversList(*entry.op.Servers)
			}
			target.SetOperation(entry.method, item.GetOperation(entry.method))
		} else {
			target := webhookItems[entry.webhook]
			if target == nil {
				target = &PathItem{Summary: item.Summary, Description: item.Description, Servers: item.Servers, Parameters: item.Parameters, Extensions: item.Extensions}
				webhookItems[entry.webhook] = target
			}
			if len(target.Servers) == 0 && entry.op.Servers != nil {
				target.Servers = c.serversList(*entry.op.Servers)
			}
			target.SetOperation(entry.method, item.GetOperation(entry.method))
		}
	}
	for _, path := range componentNames(pathItems) {
		out.Paths.Set(path, pathItems[path])
	}
	for _, name := range componentNames(webhookItems) {
		out.Webhooks[name] = webhookItems[name]
	}
	return out, nil
}

func (c *pruneCloner) buildComponentPlaceholders(out *T) {
	out.Components = &Components{}
	c.components = out.Components
	for _, target := range c.sortedTargets() {
		ref := &SchemaRef{Ref: c.componentRef(pruneSchemas, target.name)}
		switch target.kind {
		case pruneSchemas:
			if out.Components.Schemas == nil {
				out.Components.Schemas = Schemas{}
			}
			out.Components.Schemas[target.name] = &SchemaRef{}
		case pruneParameters:
			if out.Components.Parameters == nil {
				out.Components.Parameters = ParametersMap{}
			}
			out.Components.Parameters[target.name] = &ParameterRef{}
		case pruneHeaders:
			if out.Components.Headers == nil {
				out.Components.Headers = Headers{}
			}
			out.Components.Headers[target.name] = &HeaderRef{}
		case pruneRequestBodies:
			if out.Components.RequestBodies == nil {
				out.Components.RequestBodies = RequestBodies{}
			}
			out.Components.RequestBodies[target.name] = &RequestBodyRef{}
		case pruneResponses:
			if out.Components.Responses == nil {
				out.Components.Responses = ResponseBodies{}
			}
			out.Components.Responses[target.name] = &ResponseRef{}
		case pruneSecuritySchemes:
			if out.Components.SecuritySchemes == nil {
				out.Components.SecuritySchemes = SecuritySchemes{}
			}
			out.Components.SecuritySchemes[target.name] = &SecuritySchemeRef{}
		case pruneExamples:
			if out.Components.Examples == nil {
				out.Components.Examples = Examples{}
			}
			out.Components.Examples[target.name] = &ExampleRef{}
		case pruneLinks:
			if out.Components.Links == nil {
				out.Components.Links = Links{}
			}
			out.Components.Links[target.name] = &LinkRef{}
		case pruneCallbacks:
			if out.Components.Callbacks == nil {
				out.Components.Callbacks = Callbacks{}
			}
			out.Components.Callbacks[target.name] = &CallbackRef{}
		}
		_ = ref
	}
}

func (c *pruneCloner) sortedTargets() []*pruneTarget {
	targets := slices.Collect(maps.Values(c.p.targets))
	slices.SortFunc(targets, func(a, b *pruneTarget) int {
		return slices.Compare([]string{string(a.kind), a.name}, []string{string(b.kind), b.name})
	})
	return targets
}

func (c *pruneCloner) componentRef(kind pruneKind, name string) string {
	return "#/components/" + string(kind) + "/" + escapeRefString(name)
}

func (c *pruneCloner) populateComponents() error {
	for _, target := range c.sortedTargets() {
		var err error
		switch target.kind {
		case pruneSchemas:
			c.components.Schemas[target.name].Value = c.schema(target.schema)
		case pruneParameters:
			c.components.Parameters[target.name].Value = c.parameter(target.param)
		case pruneHeaders:
			c.components.Headers[target.name].Value = c.header(target.header)
		case pruneRequestBodies:
			c.components.RequestBodies[target.name].Value = c.requestBody(target.request)
		case pruneResponses:
			source := refURLFromKey(target.key)
			if source != nil {
				source.Fragment = ""
			}
			c.components.Responses[target.name].Value, err = c.response(target.response, source)
		case pruneSecuritySchemes:
			c.components.SecuritySchemes[target.name].Value = c.securityScheme(target.security)
		case pruneExamples:
			c.components.Examples[target.name].Value = c.example(target.example)
		case pruneLinks:
			c.components.Links[target.name].Value, err = c.link(target.link, refURLFromKey(target.key), nil)
		case pruneCallbacks:
			c.components.Callbacks[target.name].Value, err = c.callback(target.callback, refURLFromKey(target.key))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *pruneCloner) selectedPathItem(entry *pruneOperation) (*PathItem, error) {
	c.source = entry.source
	item, _, _, err := c.p.resolvePathItem(entry.pathItem(), entry.source, nil)
	if err != nil {
		return nil, err
	}
	return c.pathItem(item, map[string]*Operation{entry.method: entry.op})
}

func (c *pruneCloner) pathItem(item *PathItem, selected map[string]*Operation) (*PathItem, error) {
	out := &PathItem{
		Summary:     item.Summary,
		Description: item.Description,
		Servers:     c.serversList(item.Servers),
		Extensions:  maps.Clone(item.Extensions),
	}
	if len(item.Parameters) > 0 {
		out.Parameters = make(Parameters, 0, len(item.Parameters))
		for _, parameter := range item.Parameters {
			cloned, err := c.parameterRef(parameter)
			if err != nil {
				return nil, err
			}
			out.Parameters = append(out.Parameters, cloned)
		}
	}
	for _, method := range componentNames(item.Operations()) {
		if selected != nil && selected[method] == nil {
			continue
		}
		op, err := c.operation(item.Operations()[method])
		if err != nil {
			return nil, err
		}
		out.SetOperation(method, op)
	}
	return out, nil
}

func (c *pruneCloner) operation(op *Operation) (*Operation, error) {
	out := &Operation{
		Tags:         slices.Clone(op.Tags),
		Summary:      op.Summary,
		Description:  op.Description,
		OperationID:  op.OperationID,
		Deprecated:   op.Deprecated,
		ExternalDocs: op.ExternalDocs,
		Extensions:   maps.Clone(op.Extensions),
	}
	if len(op.Parameters) > 0 {
		out.Parameters = make(Parameters, 0, len(op.Parameters))
		for _, parameter := range op.Parameters {
			cloned, err := c.parameterRef(parameter)
			if err != nil {
				return nil, err
			}
			out.Parameters = append(out.Parameters, cloned)
		}
	}
	var err error
	if op.RequestBody != nil {
		out.RequestBody, err = c.requestBodyRef(op.RequestBody)
		if err != nil {
			return nil, err
		}
	}
	if op.Responses != nil {
		out.Responses = NewResponsesWithCapacity(op.Responses.Len())
		for _, code := range op.Responses.Keys() {
			response, err := c.responseRef(op.Responses.Value(code), c.source)
			if err != nil {
				return nil, err
			}
			out.Responses.Set(code, response)
		}
	}
	if len(op.Callbacks) > 0 {
		out.Callbacks = Callbacks{}
		for _, name := range componentNames(op.Callbacks) {
			callback, err := c.callbackRef(op.Callbacks[name])
			if err != nil {
				return nil, err
			}
			if callback != nil {
				out.Callbacks[name] = callback
			}
		}
	}
	if op.Security != nil {
		out.Security = c.securityRequirementsPtr(*op.Security)
	} else {
		source := c.p.doc.url
		if entry := c.p.included[op]; entry != nil && entry.source != nil {
			source = entry.source
		}
		if sourceDoc := c.p.documentAt(source); sourceDoc != nil {
			out.Security = c.securityRequirementsPtr(sourceDoc.Security)
		}
	}
	if op.Servers != nil {
		servers := c.serversList(*op.Servers)
		out.Servers = &servers
	}
	return out, nil
}

func (c *pruneCloner) info(info *Info) *Info {
	if info == nil {
		return nil
	}
	return &Info{
		Extensions:     maps.Clone(info.Extensions),
		Title:          info.Title,
		Summary:        info.Summary,
		Description:    info.Description,
		TermsOfService: info.TermsOfService,
		Contact:        info.Contact,
		License:        info.License,
		Version:        info.Version,
	}
}

func (c *pruneCloner) serversList(servers Servers) Servers {
	if len(servers) == 0 {
		return nil
	}
	out := make(Servers, len(servers))
	for i, server := range servers {
		out[i] = c.server(server)
	}
	return out
}

func (c *pruneCloner) server(server *Server) *Server {
	if server == nil {
		return nil
	}
	if cached := c.servers[server]; cached != nil {
		return cached
	}
	out := &Server{Extensions: maps.Clone(server.Extensions), URL: server.URL, Description: server.Description}
	c.servers[server] = out
	if len(server.Variables) > 0 {
		out.Variables = make(ServerVariables, len(server.Variables))
		for _, name := range componentNames(server.Variables) {
			value := server.Variables[name]
			out.Variables[name] = &ServerVariable{Extensions: maps.Clone(value.Extensions), Enum: slices.Clone(value.Enum), Default: value.Default, Description: value.Description}
		}
	}
	return out
}

func (c *pruneCloner) securityRequirements(requirements SecurityRequirements) SecurityRequirements {
	if len(requirements) == 0 {
		return nil
	}
	out := make(SecurityRequirements, len(requirements))
	for i := range requirements {
		out[i] = maps.Clone(requirements[i])
	}
	return out
}

func (c *pruneCloner) securityRequirementsPtr(requirements SecurityRequirements) *SecurityRequirements {
	out := c.securityRequirements(requirements)
	if out == nil {
		return nil
	}
	return &out
}

func (c *pruneCloner) targetByRefURL(kind pruneKind, refURL *url.URL) *pruneTarget {
	return c.p.targets[targetKey(kind, refURL)]
}
