package openapi3link

import (
	"fmt"
	"slices"
	"strings"

	"github.com/go-openapi/jsonpointer"

	"github.com/getkin/kin-openapi/openapi3"
)

// Document pairs a loaded OpenAPI document with the URI it was loaded from.
// The URI is used to resolve operationRef values that name other documents.
type Document struct {
	// URI identifies the document for operationRef resolution. It is
	// compared literally against the URI part of an operationRef, so use the
	// same normalization (for example the result of loader resolution) when
	// constructing both. It may be empty for a single in-memory document.
	URI string

	// T is the loaded document.
	T *openapi3.T
}

// Documents is a caller-provided collection of already-loaded documents.
// The evaluator never loads or fetches a document.
type Documents struct {
	docs       []Document
	byURI      map[string]*openapi3.T
	operations map[string][]operationLocation
}

type operationLocation struct {
	doc       *openapi3.T
	docURI    string
	path      string
	method    string
	operation *openapi3.Operation
}

// DuplicateOperationIDError reports that an operationId is not unique across
// the document collection, as required by the OpenAPI specification.
type DuplicateOperationIDError struct {
	OperationID string
	Locations   []string
}

func (e *DuplicateOperationIDError) Error() string {
	return fmt.Sprintf("duplicate operationId %q found at: %s", e.OperationID, strings.Join(e.Locations, "; "))
}

// OperationNotFoundError reports that a Link's operationId resolves to no
// operation in the provided documents.
type OperationNotFoundError struct {
	OperationID string
}

func (e *OperationNotFoundError) Error() string {
	return fmt.Sprintf("operationId %q not found in the provided documents", e.OperationID)
}

// OperationRefError reports that an operationRef cannot be resolved or does
// not point at an Operation Object under the document's paths.
type OperationRefError struct {
	OperationRef string
	Offset       int
	Reason       string
}

func (e *OperationRefError) Error() string {
	return fmt.Sprintf("invalid operationRef %q at offset %d: %s", e.OperationRef, e.Offset, e.Reason)
}

// NewDocuments indexes the supplied loaded documents. Documents are kept in
// the order given, which makes resolution deterministic.
//
// It returns *DuplicateOperationIDError if the same operationId is declared
// more than once across the collection.
func NewDocuments(documents ...Document) (*Documents, error) {
	collection := &Documents{
		docs:       make([]Document, 0, len(documents)),
		byURI:      make(map[string]*openapi3.T, len(documents)),
		operations: make(map[string][]operationLocation),
	}
	for _, document := range documents {
		if document.T == nil {
			continue
		}
		collection.docs = append(collection.docs, document)
		if document.URI != "" {
			if _, exists := collection.byURI[document.URI]; exists {
				return nil, fmt.Errorf("document URI %q registered more than once", document.URI)
			}
			collection.byURI[document.URI] = document.T
		}
		collection.indexDocument(document)
	}

	ids := make([]string, 0, len(collection.operations))
	for id := range collection.operations {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		locations := collection.operations[id]
		if len(locations) > 1 {
			labels := make([]string, 0, len(locations))
			for _, loc := range locations {
				labels = append(labels, formatLocation(loc))
			}
			return nil, &DuplicateOperationIDError{OperationID: id, Locations: labels}
		}
	}
	return collection, nil
}

func formatLocation(loc operationLocation) string {
	method := strings.ToLower(loc.method)
	if loc.docURI != "" {
		return loc.docURI + "#" + loc.path + "/" + method
	}
	return loc.path + "/" + method
}

func (docs *Documents) indexDocument(document Document) {
	if document.T.Paths == nil {
		return
	}
	for _, path := range document.T.Paths.Keys() {
		pathItem := document.T.Paths.Value(path)
		if pathItem == nil {
			continue
		}
		methods := pathItem.Operations()
		methodNames := make([]string, 0, len(methods))
		for method := range methods {
			methodNames = append(methodNames, method)
		}
		slices.Sort(methodNames)
		for _, method := range methodNames {
			operation := methods[method]
			if operation == nil || operation.OperationID == "" {
				continue
			}
			docs.operations[operation.OperationID] = append(
				docs.operations[operation.OperationID],
				operationLocation{
					doc:       document.T,
					docURI:    document.URI,
					path:      path,
					method:    method,
					operation: operation,
				},
			)
		}
	}
}

// primaryDoc returns the default document for fragment-only operationRefs:
// the document registered with sourceURI if any, otherwise the first one.
func (docs *Documents) primaryDoc(sourceURI string) *openapi3.T {
	if sourceURI != "" {
		if doc := docs.byURI[sourceURI]; doc != nil {
			return doc
		}
	}
	if len(docs.docs) > 0 {
		return docs.docs[0].T
	}
	return nil
}

func (docs *Documents) resolveByOperationID(operationID string) (operationLocation, error) {
	locations := docs.operations[operationID]
	if len(locations) == 0 {
		return operationLocation{}, &OperationNotFoundError{OperationID: operationID}
	}
	// NewDocuments rejects duplicates, so exactly one location exists.
	return locations[0], nil
}

// resolveByOperationRef resolves an operationRef of the form
// "[uri]#/paths/~1users/{method}". The URI part selects one of the provided
// documents; an empty or fragment-only ref resolves in the source document.
func (docs *Documents) resolveByOperationRef(ref, sourceURI string) (operationLocation, error) {
	fail := func(offset int, reason string) (operationLocation, error) {
		return operationLocation{}, &OperationRefError{OperationRef: ref, Offset: offset, Reason: reason}
	}

	hash := strings.IndexByte(ref, '#')
	if hash < 0 {
		return fail(len(ref), "operationRef must contain a '#' followed by a JSON Pointer")
	}

	uriPart := ref[:hash]
	fragment := ref[hash+1:]

	var doc *openapi3.T
	docURI := uriPart
	if uriPart == "" {
		doc = docs.primaryDoc(sourceURI)
		docURI = sourceURI
	} else {
		doc = docs.byURI[uriPart]
		if doc == nil {
			return fail(0, fmt.Sprintf("document %q is not part of the provided documents", uriPart))
		}
	}
	if doc == nil {
		return fail(0, "no documents available to resolve the reference")
	}
	if fragment == "" {
		return fail(hash+1, "empty JSON Pointer; expected '/paths/{path}/{method}'")
	}

	pointer, err := jsonpointer.New(fragment)
	if err != nil {
		return fail(hash+1, "invalid JSON Pointer: "+err.Error())
	}
	tokens := pointer.DecodedTokens()
	if len(tokens) != 3 || tokens[0] != "paths" {
		return fail(hash+1, "pointer must have the form /paths/{path}/{method}")
	}

	// tokens[1] is the decoded JSON Pointer token: the paths-object key,
	// which itself begins with '/'.
	pathKey := tokens[1]
	method := strings.ToUpper(tokens[2])

	if doc.Paths == nil {
		return fail(hash+1, "document declares no paths")
	}
	pathItem := doc.Paths.Value(pathKey)
	if pathItem == nil {
		return fail(hash+1, fmt.Sprintf("path %q does not exist in the document", pathKey))
	}
	operation := pathItem.GetOperation(method)
	if operation == nil {
		return fail(hash+1, fmt.Sprintf("operation %s %s does not exist", method, pathKey))
	}

	return operationLocation{
		doc:       doc,
		docURI:    docURI,
		path:      pathKey,
		method:    method,
		operation: operation,
	}, nil
}

// resolveLinkTarget selects the operation addressed by a Link. Exactly one of
// operationId/operationRef must be set; callers generally obtain validated
// Links from the openapi3 package.
func (docs *Documents) resolveLinkTarget(link *openapi3.Link, sourceURI string) (operationLocation, error) {
	switch {
	case link.OperationID != "" && link.OperationRef != "":
		return operationLocation{}, fmt.Errorf("link must not set both operationId and operationRef")
	case link.OperationID != "":
		return docs.resolveByOperationID(link.OperationID)
	case link.OperationRef != "":
		return docs.resolveByOperationRef(link.OperationRef, sourceURI)
	default:
		return operationLocation{}, fmt.Errorf("link requires an operationId or operationRef")
	}
}
