package link

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// expr is a parsed OpenAPI 3.0 runtime expression.
//
// See https://github.com/OAI/OpenAPI-Specification/blob/main/versions/3.0.3.md#runtime-expressions
type expr struct {
	raw string

	// root is one of "url", "method", "statusCode", "request", "response".
	root string

	// part is one of "path", "query", "header", "body", and only set for
	// the request/response roots.
	part string

	// name is the path/query/header parameter name; empty for body.
	name string
	// decodedName is the percent-decoded parameter name for lookup.
	decodedName string

	// pointer is the decoded JSON Pointer (RFC 6901) applied to a body.
	// Empty means the whole body is selected.
	pointer []string
}

// parseExpression parses an OpenAPI 3.0 runtime expression. It never turns an
// unsupported form into a constant: any string starting with "$" that cannot
// be fully consumed is rejected with the exact byte offset of the problem.
func parseExpression(raw string) (expr, *ExprError) {
	e := expr{raw: raw}
	p := &parser{s: raw}

	switch {
	case p.consume("$url"):
		e.root = "url"
	case p.consume("$method"):
		e.root = "method"
	case p.consume("$statusCode"):
		e.root = "statusCode"
	case p.consume("$request."):
		e.root = "request"
	case p.consume("$response."):
		e.root = "response"
	default:
		return expr{}, &ExprError{Expr: raw, Pos: 1, Reason: "unknown expression source; supported: $url, $method, $statusCode, $request.*, $response.*"}
	}

	if e.root == "request" || e.root == "response" {
		part, pos := p.sourcePart()
		if part == "" {
			return expr{}, &ExprError{Expr: raw, Pos: pos, Reason: "expected one of path, query, header, body"}
		}
		e.part = part

		if e.part == "body" {
			if p.left() > 0 {
				if p.s[p.i] != '#' {
					return expr{}, &ExprError{Expr: raw, Pos: p.i, Reason: "expected '#' followed by a JSON Pointer"}
				}
				tokens, errPos, err := parsePointer(p.s[p.i+1:], p.i+1)
				if err != nil {
					return expr{}, &ExprError{Expr: raw, Pos: errPos, Reason: err.Error()}
				}
				e.pointer = tokens
				p.i = len(p.s)
			}
		} else {
			if e.root == "response" && part != "header" {
				return expr{}, &ExprError{Expr: raw, Pos: 9, Reason: "OpenAPI 3.0 runtime expressions only support $response.header and $response.body"}
			}
			name, pos := p.nameToken()
			if name == "" {
				return expr{}, &ExprError{Expr: raw, Pos: pos, Reason: "missing " + e.part + " parameter name"}
			}
			e.name = name
			decoded, derr := url.QueryUnescape(name)
			if derr != nil {
				return expr{}, &ExprError{Expr: raw, Pos: pos, Reason: "invalid percent-encoding in parameter name"}
			}
			e.decodedName = decoded
			if p.left() > 0 {
				return expr{}, &ExprError{Expr: raw, Pos: p.i, Reason: "unexpected trailing characters after parameter name"}
			}
		}
	}

	if p.left() > 0 {
		return expr{}, &ExprError{Expr: raw, Pos: p.i, Reason: "unexpected trailing characters"}
	}
	return e, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) left() int { return len(p.s) - p.i }

func (p *parser) consume(prefix string) bool {
	if strings.HasPrefix(p.s[p.i:], prefix) {
		p.i += len(prefix)
		return true
	}
	return false
}

// sourcePart consumes a "path.", "query.", "header." or "body" token. The
// dot is required for the parameter parts.
func (p *parser) sourcePart() (string, int) {
	for _, part := range []string{"path", "query", "header", "body"} {
		if strings.HasPrefix(p.s[p.i:], part) {
			next := p.i + len(part)
			if part == "body" {
				p.i = next
				return part, p.i
			}
			if next < len(p.s) && p.s[next] == '.' {
				p.i = next + 1
				return part, p.i
			}
			return "", next
		}
	}
	return "", p.i
}

// nameToken consumes a parameter name. Header names follow the RFC 7230
// token syntax; path and query names accept the wider pchar style of the
// OpenAPI grammar (including percent-encoding).
func (p *parser) nameToken() (string, int) {
	start := p.i
	header := p.s[:p.i] // consumed text ends in "header."
	isHeader := strings.HasSuffix(header, "header.")
	for p.i < len(p.s) {
		c := p.s[p.i]
		if isHeader {
			if !isHeaderNameChar(c) {
				break
			}
		} else if !isPathQueryNameChar(c) {
			break
		}
		p.i++
	}
	if p.i == start {
		return "", start
	}
	return p.s[start:p.i], start
}

func isHeaderNameChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

func isPathQueryNameChar(c byte) bool {
	return isHeaderNameChar(c) ||
		c == '/' || c == '?' || c == ':' || c == '@' ||
		c == '=' || c == '(' || c == ')' || c == '[' || c == ']'
}

// parsePointer parses a JSON Pointer fragment (RFC 6901, section 3). The
// empty fragment denotes the document root. errPos is absolute within the
// original expression.
func parsePointer(fragment string, base int) ([]string, int, error) {
	if fragment == "" {
		return nil, base, nil
	}
	if fragment[0] != '/' {
		return nil, base, fmt.Errorf("JSON Pointer fragment must start with '/' or be empty")
	}
	var tokens []string
	for i := 0; i < len(fragment); {
		i++ // skip '/'
		start := i
		for i < len(fragment) && fragment[i] != '/' {
			i++
		}
		token, err := unescapePointerToken(fragment[start:i])
		if err != nil {
			return nil, base + start, err
		}
		tokens = append(tokens, token)
	}
	return tokens, base, nil
}

func unescapePointerToken(raw string) (string, error) {
	if !strings.ContainsAny(raw, "~%") {
		return raw, nil
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch c {
		case '~':
			if i+1 >= len(raw) {
				return "", fmt.Errorf("invalid JSON Pointer escape: trailing '~'")
			}
			switch raw[i+1] {
			case '0':
				b.WriteByte('~')
			case '1':
				b.WriteByte('/')
			default:
				return "", fmt.Errorf("invalid JSON Pointer escape: %q", raw[i:i+2])
			}
			i++
		case '%':
			if i+2 >= len(raw) {
				return "", fmt.Errorf("invalid percent-encoding: %q", raw[i:])
			}
			v, err := strconv.ParseUint(raw[i+1:i+3], 16, 8)
			if err != nil {
				return "", fmt.Errorf("invalid percent-encoding: %q", raw[i:i+3])
			}
			b.WriteByte(byte(v))
			i += 2
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

// evalContext holds the observed request/response exchange an expression is
// evaluated against.
type evalContext struct {
	req *Request
	res *Response
}

// errMissing is turned into MissingError at the call site; it is never
// returned to callers.
var errMissing = fmt.Errorf("missing")

func (x expr) eval(env *evalContext) (any, error) {
	switch x.root {
	case "url":
		if env.req == nil || env.req.URL == nil {
			return nil, errMissing
		}
		return env.req.URL.String(), nil
	case "method":
		if env.req == nil || env.req.Method == "" {
			return nil, errMissing
		}
		return strings.ToUpper(env.req.Method), nil
	case "statusCode":
		if env.res == nil {
			return nil, errMissing
		}
		return env.res.StatusCode, nil
	}

	switch x.part {
	case "path":
		v, ok := requestPathValue(env.req, x.lookupName())
		if !ok {
			return nil, errMissing
		}
		return v, nil
	case "query":
		v, ok := requestQueryValue(env.req, x.lookupName())
		if !ok {
			return nil, errMissing
		}
		return v, nil
	case "header":
		var h http.Header
		if x.root == "request" {
			h = requestHeaders(env.req)
		} else if env.res != nil {
			h = env.res.Header
		}
		v, ok := headerValue(h, x.lookupName())
		if !ok {
			return nil, errMissing
		}
		return v, nil
	case "body":
		var body any
		switch x.root {
		case "request":
			if env.req == nil {
				return nil, errMissing
			}
			body = env.req.Body
		case "response":
			if env.res == nil {
				return nil, errMissing
			}
			body = env.res.Body
		}
		if len(x.pointer) == 0 {
			if body == nil {
				return nil, errMissing
			}
			return body, nil
		}
		return jsonPointerWalk(body, x.pointer)
	}
	return nil, fmt.Errorf("unsupported expression part %q", x.part)
}

func requestPathValue(req *Request, name string) (any, bool) {
	if req == nil || req.Path == nil {
		return nil, false
	}
	v, ok := req.Path[name]
	return v, ok
}

func (x expr) lookupName() string {
	if x.decodedName != "" {
		return x.decodedName
	}
	return x.name
}

// requestQueryValue returns a string for a single occurrence and []string
// for repeated occurrences.
func requestQueryValue(req *Request, name string) (any, bool) {
	var values []string
	if req != nil && req.URL != nil {
		values = req.URL.Query()[name]
	}
	if len(values) == 0 && req != nil {
		values = req.Query[name]
	}
	switch len(values) {
	case 0:
		return nil, false
	case 1:
		return values[0], true
	default:
		out := make([]string, len(values))
		copy(out, values)
		return out, true
	}
}

func requestHeaders(req *Request) http.Header {
	if req == nil {
		return nil
	}
	return req.Header
}

// headerValue returns a single string or []string for multi-valued headers.
// Lookup is fully case-insensitive, as required by HTTP; this also handles
// maps whose keys were not stored in Go's canonical header form.
func headerValue(h http.Header, name string) (any, bool) {
	if len(h) == 0 {
		return nil, false
	}
	var values []string
	lower := strings.ToLower(name)
	for k, vs := range h {
		if strings.ToLower(k) == lower {
			values = append(values, vs...)
		}
	}
	switch len(values) {
	case 0:
		return nil, false
	case 1:
		return values[0], true
	default:
		out := make([]string, len(values))
		copy(out, values)
		return out, true
	}
}

// jsonPointerWalk walks decoded JSON data per RFC 6901. A target that exists
// with JSON null returns (nil, nil); a target that does not exist returns
// errMissing.
func jsonPointerWalk(root any, tokens []string) (any, error) {
	cur := root
	for _, token := range tokens {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[token]
			if !ok {
				return nil, errMissing
			}
			cur = v
		case []any:
			if token == "-" {
				return nil, errMissing
			}
			idx, err := strconv.Atoi(token)
			if err != nil || idx < 0 || idx >= len(node) || (len(token) > 1 && token[0] == '0') {
				return nil, fmt.Errorf("invalid array index %q in JSON Pointer", token)
			}
			cur = node[idx]
		default:
			return nil, errMissing
		}
	}
	return cur, nil
}
