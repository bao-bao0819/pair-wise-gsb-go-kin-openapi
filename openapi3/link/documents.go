package link

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Target is the operation a Link points at, with the path template and HTTP
// method it is declared under.
type Target struct {
	Document  *openapi3.T
	DocURL    string
	Path      string
	Method    string
	PathItem  *openapi3.PathItem
	Operation *openapi3.Operation
}

// operationOccurrence is one operationId declaration inside a document.
type operationOccurrence struct {
	id     string
	target Target
}

// Documents is a caller-provided collection of already-loaded OpenAPI
// documents. Keys are document URLs (any stable identifier works for a
// single document); relative operationRef values are resolved against them.
type Documents struct {
	byURL       map[string]*openapi3.T
	docURLs     map[*openapi3.T]string
	operationID map[string]map[*openapi3.T]operationOccurrence
}

// NewDocuments indexes the loaded documents. operationId must be unique
// within each document; a violation is reported rather than resolved
// ambiguously.
func NewDocuments(docs map[string]*openapi3.T) (*Documents, error) {
	d := &Documents{
		byURL:       make(map[string]*openapi3.T, len(docs)),
		docURLs:     make(map[*openapi3.T]string, len(docs)),
		operationID: make(map[string]map[*openapi3.T]operationOccurrence),
	}

	urls := make([]string, 0, len(docs))
	for u := range docs {
		urls = append(urls, u)
	}
	sort.Strings(urls)

	for _, u := range urls {
		doc := docs[u]
		if doc == nil {
			return nil, fmt.Errorf("openapi3/link: nil document registered for %q", u)
		}
		if _, dup := d.docURLs[doc]; dup {
			return nil, fmt.Errorf("openapi3/link: document %q is also registered as %q", u, d.docURLs[doc])
		}
		d.byURL[u] = doc
		d.docURLs[doc] = u

		var errs []error
		for _, occ := range indexOperations(doc, u) {
			byDoc := d.operationID[occ.id]
			if byDoc == nil {
				byDoc = make(map[*openapi3.T]operationOccurrence)
				d.operationID[occ.id] = byDoc
			}
			if first, ok := byDoc[doc]; ok {
				errs = append(errs, &DuplicateOperationIDError{
					DocURL:       u,
					ID:           occ.id,
					FirstTarget:  first.target.label(),
					SecondTarget: occ.target.label(),
				})
				continue
			}
			byDoc[doc] = occ
		}
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
	}
	return d, nil
}

// indexOperations enumerates every paths operation that declares an
// operationId, in deterministic path/method order.
func indexOperations(doc *openapi3.T, docURL string) []operationOccurrence {
	var out []operationOccurrence
	if doc == nil || doc.Paths == nil {
		return out
	}
	paths := doc.Paths.Keys()
	sort.Strings(paths)
	for _, path := range paths {
		item := doc.Paths.Value(path)
		if item == nil {
			continue
		}
		for _, method := range sortedMethods(item) {
			op := item.GetOperation(method)
			if op == nil || op.OperationID == "" {
				continue
			}
			out = append(out, operationOccurrence{
				id: op.OperationID,
				target: Target{
					Document:  doc,
					DocURL:    docURL,
					Path:      path,
					Method:    method,
					PathItem:  item,
					Operation: op,
				},
			})
		}
	}
	return out
}

func sortedMethods(item *openapi3.PathItem) []string {
	methods := make([]string, 0)
	for method := range item.Operations() {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return methods
}

func (t Target) label() string {
	return t.Method + " " + t.Path
}

// resolveOperationID finds a unique operationId in the source document.
func (d *Documents) resolveOperationID(source *openapi3.T, id string) (*Target, error) {
	byDoc := d.operationID[id]
	switch {
	case len(byDoc) == 0:
		return nil, &TargetError{OperationID: id, Reason: "no operation with this operationId in the source document"}
	case source != nil:
		if occ, ok := byDoc[source]; ok {
			t := occ.target
			return &t, nil
		}
		return nil, &TargetError{OperationID: id, Reason: "operationId is declared in another document, not the source document"}
	default:
		return nil, &TargetError{OperationID: id, Reason: "no source document supplied to scope operationId"}
	}
}

// resolveOperationRef resolves a relative or absolute URL with a JSON Pointer
// fragment, e.g. "other.yaml#/paths/~1widgets/get" or "#/paths/widgets/get".
func (d *Documents) resolveOperationRef(source *openapi3.T, sourceURL, ref string) (*Target, error) {
	docURL, fragment, err := splitRef(sourceURL, ref)
	if err != nil {
		return nil, &TargetError{OperationRef: ref, Reason: err.Error()}
	}
	doc := d.byURL[docURL]
	if doc == nil {
		if source != nil {
			if u, ok := d.docURLs[source]; ok && docURL == u {
				doc = source
			}
		}
	}
	if doc == nil {
		return nil, &TargetError{OperationRef: ref, Reason: "referenced document " + strconv.Quote(docURL) + " is not loaded"}
	}

	target, err := pointerToOperation(doc, d.docURLs[doc], fragment)
	if err != nil {
		return nil, &TargetError{OperationRef: ref, Reason: err.Error()}
	}
	return target, nil
}

// splitRef resolves the document part of an operationRef against the source
// document URL and returns the JSON Pointer fragment.
func splitRef(sourceURL, ref string) (string, string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", "", fmt.Errorf("malformed operationRef URL: %w", err)
	}
	fragment := u.Fragment
	u.Fragment = ""
	u.RawFragment = ""

	fragmentOnly := u.Path == "" && u.Opaque == "" && u.Host == "" && u.RawQuery == "" && u.Scheme == ""
	absoluteRef := u.Scheme != "" || u.Host != "" || strings.HasPrefix(u.Path, "/")

	var docURL string
	if !fragmentOnly && absoluteRef {
		docURL = u.String()
	} else if base, perr := url.Parse(sourceURL); perr == nil && base != nil {
		if fragmentOnly {
			docURL = sourceURL
		} else if base.Scheme == "" && base.Host == "" {
			// Relative keys such as "dir/api.yaml": resolve the file
			// component relatively instead of treating it as root-relative.
			docURL = path.Join(path.Dir(sourceURL), u.Path)
		} else {
			// Resolve relative references against the source document's
			// directory.
			dir := *base
			if idx := strings.LastIndex(base.Path, "/"); idx >= 0 {
				dir.Path = base.Path[:idx+1]
			}
			docURL = dir.ResolveReference(u).String()
		}
	} else {
		docURL = u.String()
	}

	if fragment == "" {
		return docURL, "", fmt.Errorf("operationRef must contain a JSON Pointer fragment pointing at an operation")
	}
	if !strings.HasPrefix(fragment, "/") {
		return docURL, fragment, fmt.Errorf("operationRef fragment must be a JSON Pointer (start with '/')")
	}
	return docURL, fragment, nil
}

// pointerToOperation walks a fragment of the form /paths/{path}/{method} and
// returns the addressed operation.
func pointerToOperation(doc *openapi3.T, docURL, fragment string) (*Target, error) {
	tokens, _, err := parsePointer(fragment, 0)
	if err != nil {
		return nil, err
	}
	if len(tokens) < 3 || tokens[0] != "paths" {
		return nil, fmt.Errorf("fragment must address an operation under /paths")
	}
	if doc.Paths == nil {
		return nil, fmt.Errorf("document has no paths")
	}
	item := doc.Paths.Value(tokens[1])
	pathKey := tokens[1]
	if item == nil {
		return nil, fmt.Errorf("path %q does not exist in the document", pathKey)
	}
	method := strings.ToUpper(tokens[2])
	op := item.GetOperation(method)
	if op == nil {
		return nil, fmt.Errorf("path %q has no %s operation", pathKey, method)
	}
	return &Target{
		Document:  doc,
		DocURL:    docURL,
		Path:      pathKey,
		Method:    method,
		PathItem:  item,
		Operation: op,
	}, nil
}
