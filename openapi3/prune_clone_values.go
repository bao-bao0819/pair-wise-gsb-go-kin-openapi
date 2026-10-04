package openapi3

import (
	"maps"
	"net/url"
	"slices"
)

func (c *pruneCloner) schemaRef(ref *SchemaRef) *SchemaRef {
	if ref == nil {
		return nil
	}
	if ref.Ref != "" {
		target := c.targetForValue(pruneSchemas, c.canonicalRefPath(ref.RefPath()), ref.Value)
		if target == nil {
			return &SchemaRef{Ref: ref.Ref}
		}
		return &SchemaRef{Ref: c.componentRef(pruneSchemas, target.name), Extensions: maps.Clone(ref.Extensions)}
	}
	return &SchemaRef{Value: c.schema(ref.Value), Extensions: maps.Clone(ref.Extensions)}
}

func (c *pruneCloner) schema(schema *Schema) *Schema {
	if schema == nil {
		return nil
	}
	if cached := c.schemas[schema]; cached != nil {
		return cached
	}
	out := &Schema{
		Extensions:            maps.Clone(schema.Extensions),
		OneOf:                 SchemaRefs{},
		AnyOf:                 SchemaRefs{},
		AllOf:                 SchemaRefs{},
		Type:                  cloneTypes(schema.Type),
		Title:                 schema.Title,
		Format:                schema.Format,
		Description:           schema.Description,
		Enum:                  slices.Clone(schema.Enum),
		Default:               schema.Default,
		Example:               schema.Example,
		Examples:              slices.Clone(schema.Examples),
		ExternalDocs:          schema.ExternalDocs,
		UniqueItems:           schema.UniqueItems,
		ExclusiveMin:          schema.ExclusiveMin,
		ExclusiveMax:          schema.ExclusiveMax,
		Nullable:              schema.Nullable,
		ReadOnly:              schema.ReadOnly,
		WriteOnly:             schema.WriteOnly,
		AllowEmptyValue:       schema.AllowEmptyValue,
		Deprecated:            schema.Deprecated,
		XML:                   schema.XML,
		Min:                   schema.Min,
		Max:                   schema.Max,
		MultipleOf:            schema.MultipleOf,
		MinLength:             schema.MinLength,
		MaxLength:             schema.MaxLength,
		Pattern:               schema.Pattern,
		MinItems:              schema.MinItems,
		MaxItems:              schema.MaxItems,
		Required:              slices.Clone(schema.Required),
		Properties:            Schemas{},
		MinProps:              schema.MinProps,
		MaxProps:              schema.MaxProps,
		AdditionalProperties:  schema.AdditionalProperties,
		Discriminator:         c.discriminator(schema.Discriminator),
		Const:                 schema.Const,
		PrefixItems:           SchemaRefs{},
		PatternProperties:     Schemas{},
		DependentSchemas:      Schemas{},
		Defs:                  Schemas{},
		MinContains:           schema.MinContains,
		MaxContains:           schema.MaxContains,
		UnevaluatedItems:      schema.UnevaluatedItems,
		UnevaluatedProperties: schema.UnevaluatedProperties,
		DependentRequired:     cloneDependentRequired(schema.DependentRequired),
		Always:                schema.Always,
		SchemaDialect:         schema.SchemaDialect,
		Comment:               schema.Comment,
		SchemaID:              schema.SchemaID,
		Anchor:                schema.Anchor,
		DynamicRef:            schema.DynamicRef,
		DynamicAnchor:         schema.DynamicAnchor,
		ContentMediaType:      schema.ContentMediaType,
		ContentEncoding:       schema.ContentEncoding,
	}
	c.schemas[schema] = out
	for _, refs := range []struct {
		source SchemaRefs
		target *SchemaRefs
	}{
		{schema.AllOf, &out.AllOf},
		{schema.AnyOf, &out.AnyOf},
		{schema.OneOf, &out.OneOf},
		{schema.PrefixItems, &out.PrefixItems},
	} {
		for _, ref := range refs.source {
			*refs.target = append(*refs.target, c.schemaRef(ref))
		}
	}
	for _, name := range componentNames(schema.Properties) {
		out.Properties[name] = c.schemaRef(schema.Properties[name])
	}
	for _, name := range componentNames(schema.PatternProperties) {
		out.PatternProperties[name] = c.schemaRef(schema.PatternProperties[name])
	}
	for _, name := range componentNames(schema.DependentSchemas) {
		out.DependentSchemas[name] = c.schemaRef(schema.DependentSchemas[name])
	}
	for _, name := range componentNames(schema.Defs) {
		out.Defs[name] = c.schemaRef(schema.Defs[name])
	}
	out.Not = c.schemaRef(schema.Not)
	out.Items = c.schemaRef(schema.Items)
	out.AdditionalProperties.Schema = c.schemaRef(schema.AdditionalProperties.Schema)
	out.Contains = c.schemaRef(schema.Contains)
	out.PropertyNames = c.schemaRef(schema.PropertyNames)
	out.UnevaluatedItems.Schema = c.schemaRef(schema.UnevaluatedItems.Schema)
	out.UnevaluatedProperties.Schema = c.schemaRef(schema.UnevaluatedProperties.Schema)
	out.If = c.schemaRef(schema.If)
	out.Then = c.schemaRef(schema.Then)
	out.Else = c.schemaRef(schema.Else)
	out.ContentSchema = c.schemaRef(schema.ContentSchema)
	return out
}

func cloneDependentRequired(value map[string][]string) map[string][]string {
	if value == nil {
		return nil
	}
	out := make(map[string][]string, len(value))
	for key, values := range value {
		out[key] = slices.Clone(values)
	}
	return out
}

func (c *pruneCloner) discriminator(value *Discriminator) *Discriminator {
	if value == nil {
		return nil
	}
	out := &Discriminator{Extensions: maps.Clone(value.Extensions), PropertyName: value.PropertyName, Mapping: map[string]MappingRef{}}
	for name := range value.Mapping {
		mapping := value.Mapping[name]
		target := c.schemaTargetForMapping(mapping)
		if target != nil {
			out.Mapping[name] = MappingRef{Ref: c.componentRef(pruneSchemas, target.name)}
		} else {
			out.Mapping[name] = mapping
		}
	}
	return out
}

func cloneTypes(value *Types) *Types {
	if value == nil {
		return nil
	}
	cloned := slices.Clone(*value)
	return &cloned
}

func (c *pruneCloner) schemaTargetForMapping(mapping MappingRef) *pruneTarget {
	refPath := (*SchemaRef)(&mapping).RefPath()
	target := c.p.targets[targetKey(pruneSchemas, c.canonicalRefPath(refPath))]
	if target != nil {
		return target
	}
	for _, candidate := range c.p.targets {
		if candidate.kind == pruneSchemas && candidate.schema == mapping.Value {
			return candidate
		}
	}
	return nil
}

func (c *pruneCloner) parameter(value *Parameter) *Parameter {
	if value == nil {
		return nil
	}
	return &Parameter{
		Extensions:      maps.Clone(value.Extensions),
		Name:            value.Name,
		In:              value.In,
		Description:     value.Description,
		Style:           value.Style,
		Explode:         value.Explode,
		AllowEmptyValue: value.AllowEmptyValue,
		AllowReserved:   value.AllowReserved,
		Deprecated:      value.Deprecated,
		Required:        value.Required,
		Schema:          c.schemaRef(value.Schema),
		Example:         value.Example,
		Examples:        c.examples(value.Examples),
		Content:         c.content(value.Content),
	}
}

func (c *pruneCloner) header(value *Header) *Header {
	if value == nil {
		return nil
	}
	return &Header{Parameter: *c.parameter(&value.Parameter)}
}

func (c *pruneCloner) requestBody(value *RequestBody) *RequestBody {
	if value == nil {
		return nil
	}
	return &RequestBody{
		Extensions:  maps.Clone(value.Extensions),
		Description: value.Description,
		Required:    value.Required,
		Content:     c.content(value.Content),
	}
}

func (c *pruneCloner) response(value *Response, source *url.URL) (*Response, error) {
	if value == nil {
		return nil, nil
	}
	out := &Response{
		Extensions:  maps.Clone(value.Extensions),
		Description: value.Description,
		Content:     c.content(value.Content),
		Headers:     Headers{},
		Links:       Links{},
	}
	for _, name := range componentNames(value.Headers) {
		header, err := c.headerRef(value.Headers[name])
		if err != nil {
			return nil, err
		}
		out.Headers[name] = header
	}
	for _, name := range componentNames(value.Links) {
		ref := value.Links[name]
		link, err := c.linkRef(ref, source)
		if err != nil {
			return nil, err
		}
		if link != nil {
			out.Links[name] = link
		}
	}
	return out, nil
}

func (c *pruneCloner) content(value Content) Content {
	if value == nil {
		return nil
	}
	out := Content{}
	for _, name := range componentNames(value) {
		media := value[name]
		cloned := &MediaType{
			Extensions: maps.Clone(media.Extensions),
			Schema:     c.schemaRef(media.Schema),
			ItemSchema: c.schemaRef(media.ItemSchema),
			Example:    media.Example,
			Examples:   c.examples(media.Examples),
			Encoding:   Encodings{},
		}
		for _, property := range componentNames(media.Encoding) {
			encoding := media.Encoding[property]
			clonedEncoding := &Encoding{
				Extensions:    maps.Clone(encoding.Extensions),
				ContentType:   encoding.ContentType,
				Style:         encoding.Style,
				Explode:       encoding.Explode,
				AllowReserved: encoding.AllowReserved,
				Headers:       Headers{},
			}
			for _, headerName := range componentNames(encoding.Headers) {
				clonedEncoding.Headers[headerName], _ = c.headerRef(encoding.Headers[headerName])
			}
			cloned.Encoding[property] = clonedEncoding
		}
		out[name] = cloned
	}
	return out
}

func (c *pruneCloner) examples(value Examples) Examples {
	if value == nil {
		return nil
	}
	out := Examples{}
	for _, name := range componentNames(value) {
		out[name] = c.exampleRef(value[name])
	}
	return out
}

func (c *pruneCloner) example(value *Example) *Example {
	if value == nil {
		return nil
	}
	return &Example{
		Extensions:    maps.Clone(value.Extensions),
		Summary:       value.Summary,
		Description:   value.Description,
		Value:         value.Value,
		ExternalValue: value.ExternalValue,
	}
}

func (c *pruneCloner) securityScheme(value *SecurityScheme) *SecurityScheme {
	if value == nil {
		return nil
	}
	out := &SecurityScheme{
		Extensions:       maps.Clone(value.Extensions),
		Type:             value.Type,
		Description:      value.Description,
		Name:             value.Name,
		In:               value.In,
		Scheme:           value.Scheme,
		BearerFormat:     value.BearerFormat,
		OpenIdConnectUrl: value.OpenIdConnectUrl,
	}
	if value.Flows != nil {
		out.Flows = &OAuthFlows{Extensions: maps.Clone(value.Flows.Extensions)}
		for _, flow := range []**OAuthFlow{&value.Flows.Implicit, &value.Flows.Password, &value.Flows.ClientCredentials, &value.Flows.AuthorizationCode} {
			if *flow == nil {
				continue
			}
			cloned := &OAuthFlow{
				Extensions:       maps.Clone((*flow).Extensions),
				AuthorizationURL: (*flow).AuthorizationURL,
				TokenURL:         (*flow).TokenURL,
				RefreshURL:       (*flow).RefreshURL,
				Scopes:           maps.Clone((*flow).Scopes),
			}
			switch *flow {
			case value.Flows.Implicit:
				out.Flows.Implicit = cloned
			case value.Flows.Password:
				out.Flows.Password = cloned
			case value.Flows.ClientCredentials:
				out.Flows.ClientCredentials = cloned
			case value.Flows.AuthorizationCode:
				out.Flows.AuthorizationCode = cloned
			}
		}
	}
	return out
}
