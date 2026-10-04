package openapi3filter

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/getkin/kin-openapi/openapi3"
)

func newRequest(t *testing.T, rawurl string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawurl, nil)
	require.NoError(t, err)
	return req
}

// TestSetOnRequest_Path verifies placeholder substitution, including values
// whose encoding is normally collapsed by net/url (slashes, commas).
func TestSetOnRequest_Path(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{"a/b,c,%", "/users/a%2Fb%2Cc%2C%25/items"},
		{"héllo wörld", "/users/h%C3%A9llo%20w%C3%B6rld/items"},
		{"plain", "/users/plain/items"},
	}
	param := openapi3.NewPathParameter("id").WithSchema(openapi3.NewStringSchema())
	for _, tc := range cases {
		req := newRequest(t, "http://example.com/users/{id}/items")
		require.NoError(t, EncodeRequestParameter(req, param, tc.value))
		require.Equal(t, tc.want, req.URL.RequestURI())

		// A reparsed URL keeps the semantic and encoded path.
		reparsed, err := url.Parse(req.URL.String())
		require.NoError(t, err)
		require.Equal(t, req.URL.Path, reparsed.Path)
		require.Equal(t, req.URL.EscapedPath(), reparsed.EscapedPath())
	}

	// label and matrix styles
	for _, tc := range []struct {
		style string
		want  string
	}{
		{"label", "/users/.a%2Fb/items"},
		{"matrix", "/users/;id=a%2Fb/items"},
	} {
		p := openapi3.NewPathParameter("id")
		p.Style = tc.style
		req := newRequest(t, "http://example.com/users/{id}/items")
		require.NoError(t, EncodeRequestParameter(req, p, "a/b"))
		require.Equal(t, tc.want, req.URL.RequestURI())
	}
}

// TestSetOnRequest_PathMissingPlaceholder reports a specific parameter error
// instead of silently dropping the value.
func TestSetOnRequest_PathMissingPlaceholder(t *testing.T) {
	req := newRequest(t, "http://example.com/users/me")
	param := openapi3.NewPathParameter("id")
	err := EncodeRequestParameter(req, param, "5")
	require.Error(t, err)
	require.Contains(t, err.Error(), `parameter "id" in path`)
	require.Contains(t, err.Error(), "placeholder")
	// Request untouched.
	require.Equal(t, "/users/me", req.URL.RequestURI())
}

// TestSetOnRequest_QueryPreservesOrderAndDuplicates verifies that existing
// query entries, their order and unrelated duplicates are preserved.
func TestSetOnRequest_Query(t *testing.T) {
	req := newRequest(t, "http://example.com/path?keep=1&other=2&keep=3")

	// form explode array appends new occurrences after the existing ones.
	param := openapi3.NewQueryParameter("id")
	param.Schema = &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type:  &openapi3.Types{"array"},
		Items: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
	}}
	require.NoError(t, EncodeRequestParameter(req, param, []string{"a b", "c/d", "100%"}))

	require.Equal(t, "keep=1&other=2&keep=3&id=a+b&id=c%2Fd&id=100%25", req.URL.RawQuery)

	// A second parameter keeps appending and still does not reorder.
	param2 := openapi3.NewQueryParameter("q")
	param2.Schema = &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}
	require.NoError(t, EncodeRequestParameter(req, param2, "héllo"))
	require.Equal(t, "keep=1&other=2&keep=3&id=a+b&id=c%2Fd&id=100%25&q=h%C3%A9llo", req.URL.RawQuery)

	// The unrelated duplicate "keep" entries and their order survive parsing.
	values := req.URL.Query()
	require.Equal(t, []string{"1", "3"}, values["keep"])
	require.Equal(t, []string{"2"}, values["other"])
	require.Equal(t, []string{"a b", "c/d", "100%"}, values["id"])
	require.Equal(t, []string{"héllo"}, values["q"])
}

// TestSetOnRequest_QueryKeyEscaped verifies keys are escaped separately from
// values (a value delimiter is never treated as a structure delimiter).
func TestSetOnRequest_QueryKeyEscaped(t *testing.T) {
	req := newRequest(t, "http://example.com/path")
	param := openapi3.NewQueryParameter("a b&c")
	param.Schema = &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}
	require.NoError(t, EncodeRequestParameter(req, param, "x&y,z"))
	require.Equal(t, "a+b%26c=x%26y%2Cz", req.URL.RawQuery)

	values := req.URL.Query()
	require.Equal(t, "x&y,z", values.Get("a b&c"))
}

// TestSetOnRequest_HeaderAndCookie verifies header and cookie writing.
func TestSetOnRequest_HeaderAndCookie(t *testing.T) {
	req := newRequest(t, "http://example.com/")

	header := openapi3.NewHeaderParameter("X-My-Header")
	header.Schema = &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}
	require.NoError(t, EncodeRequestParameter(req, header, "hello/wörld"))
	require.Equal(t, "hello/wörld", req.Header.Get("X-My-Header"))

	cookie := openapi3.NewCookieParameter("session")
	explode := false
	cookie.Explode = &explode
	cookie.Schema = &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}
	require.NoError(t, EncodeRequestParameter(req, cookie, "abc123-_"))
	got, err := req.Cookie("session")
	require.NoError(t, err)
	require.Equal(t, "abc123-_", got.Value)
}

// TestEncodeParameter_AllowReserved verifies allowReserved scope and effect.
func TestEncodeParameter_AllowReserved(t *testing.T) {
	// Query: reserved characters are left unescaped, space is still encoded.
	param := &openapi3.Parameter{
		Name:          "q",
		In:            openapi3.ParameterInQuery,
		AllowReserved: true,
		Schema:        &openapi3.SchemaRef{Value: openapi3.NewStringSchema()},
	}
	enc, err := EncodeParameter(param, "a/b?c=1,d=2 e%f")
	require.NoError(t, err)
	require.Len(t, enc.QueryPairs, 1)
	wire := enc.QueryPairs[0].QueryKey + "=" + escapeQueryValue(enc.QueryPairs[0].QueryValue, true)
	require.Equal(t, "q=a/b?c=1,d=2%20e%25f", wire)

	// Decoding still yields the original value.
	values, err := url.ParseQuery(wire)
	require.NoError(t, err)
	require.Equal(t, "a/b?c=1,d=2 e%f", values.Get("q"))

	// Non-ASCII text is percent-encoded as UTF-8 even with allowReserved.
	enc2, err := EncodeParameter(param, "hé/世界")
	require.NoError(t, err)
	wire2 := enc2.QueryPairs[0].QueryKey + "=" + escapeQueryValue(enc2.QueryPairs[0].QueryValue, true)
	require.Equal(t, "q=h%C3%A9/%E4%B8%96%E7%95%8C", wire2)
	values2, err := url.ParseQuery(wire2)
	require.NoError(t, err)
	require.Equal(t, "hé/世界", values2.Get("q"))

	// allowReserved is invalid outside query parameters.
	pathParam := openapi3.NewPathParameter("id")
	pathParam.AllowReserved = true
	_, err = EncodeParameter(pathParam, "a/b")
	require.Error(t, err)
	require.Contains(t, err.Error(), "allowReserved")

	headerParam := openapi3.NewHeaderParameter("X")
	headerParam.AllowReserved = true
	_, err = EncodeParameter(headerParam, "a/b")
	require.Error(t, err)
}
