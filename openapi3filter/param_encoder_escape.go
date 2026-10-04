package openapi3filter

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"
)

// escapePathValue percent-encodes a single path parameter value. Structural
// delimiters are added later, unescaped, so a delimiter inside a value cannot
// be confused with the structure (values are checked separately where needed).
func escapePathValue(value string) string {
	return url.PathEscape(value)
}

func escapeAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = url.PathEscape(v)
	}
	return out
}

// queryEscapeKey percent-encodes a query key. Keys never honor allowReserved.
func queryEscapeKey(key string) string {
	return url.QueryEscape(key)
}

// escapeQueryValue percent-encodes a query value. When allowReserved is true
// the RFC 3986 reserved set is left untouched, as allowed by the
// allowReserved property (query parameters only). A literal "%" is still
// encoded because it introduces a percent-escape.
func escapeQueryValue(value string, allowReserved bool) string {
	if !allowReserved {
		return url.QueryEscape(value)
	}
	return escapeQueryAllowReserved(value)
}

// queryReservedChars is the RFC 3986 reserved set. "%" is a delimiter for
// percent-encoding rather than reserved data: a literal percent sign is always
// encoded so a parser cannot mistake it for an escape triplet.
const queryReservedChars = ":/?#[]@!$&'()*+,;="

func escapeQueryAllowReserved(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r < utf8.RuneSelf {
			c := byte(r)
			if isUnreserved(c) || isReserved(c) {
				b.WriteByte(c)
				continue
			}
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xf])
			continue
		}
		// Encode non-ASCII runes as their UTF-8 bytes.
		var buf [utf8.UTFMax]byte
		n := utf8.EncodeRune(buf[:], r)
		for _, c := range buf[:n] {
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xf])
		}
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	case c == '-', c == '.', c == '_', c == '~':
		return true
	}
	return false
}

func isReserved(c byte) bool {
	return strings.IndexByte(queryReservedChars, c) >= 0
}

const upperHex = "0123456789ABCDEF"

// safeHeaderValue rejects bytes that are not legal in an HTTP header field
// value (RFC 7230 field-content: VCHAR / obs-text / SP / HTAB).
func safeHeaderValue(param *openapi3.Parameter, value string) (string, *ParseError) {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\t' {
			continue
		}
		if c < ' ' || c == 0x7f {
			return "", parameterEncodingError(param, KindInvalidFormat,
				"value contains a control character that is not legal in an HTTP header")
		}
	}
	return value, nil
}

// safeCookieValue rejects bytes that net/http silently drops from a cookie, so
// the encoder reports a specific parameter error instead of losing data.
// Cookie production (RFC 6265 cookie-octet) accepts VCHAR excluding
// whitespace, DQUOTE, comma, semicolon and backslash; non-ASCII is rejected.
func safeCookieValue(param *openapi3.Parameter, value string) (string, *ParseError) {
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch c {
		case ' ', '\t', '"', ';', '\\':
			return "", parameterEncodingError(param, KindInvalidFormat,
				cookieByteReason(c))
		}
		if c < 0x21 || c > 0x7e {
			return "", parameterEncodingError(param, KindInvalidFormat,
				"value contains a byte that cannot be represented in a cookie (percent-encode it before encoding)")
		}
	}
	return value, nil
}

func cookieByteReason(c byte) string {
	switch c {
	case ' ':
		return "cookie value contains a space"
	case '\t':
		return "cookie value contains a tab"
	case '"':
		return `cookie value contains a double quote`
	case ';':
		return `cookie value contains a semicolon (cookie attribute separator)`
	case '\\':
		return `cookie value contains a backslash`
	}
	return "cookie value contains a byte that net/http would drop"
}
