package link

import (
	"net/http"
	"net/url"
)

// Request is the observed request of an exchange. It is input to runtime
// expression evaluation; the evaluator never performs network I/O.
//
// URL carries the request URL: its path feeds $url and its query component
// feeds $request.query.*. Path template parameters are not recoverable from a
// concrete URL, so they are supplied explicitly via Path. Header lookups are
// case-insensitive. Body is JSON decoded (encoding/json's any) when the
// expression touches $request.body.
type Request struct {
	Method string
	URL    *url.URL
	Header http.Header
	Path   map[string]any
	Query  map[string][]string
	Body   any
}

// Response is the observed response of an exchange. Header lookups are
// case-insensitive. StatusCode feeds $statusCode; Body (JSON decoded) feeds
// $response.body.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       any
}

// Exchange is the request/response pair captured by a test tool.
type Exchange struct {
	Request  *Request
	Response *Response
}
