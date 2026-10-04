package openapi3filter

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// validHeaderFieldValue reports whether s is a legal HTTP field value per
// RFC 9110: visible US-ASCII (0x21-0x7E) plus SP and HTAB. Encoding never
// percent-escapes header values, so illegal bytes (notably non-ASCII) must be
// rejected instead of being mangled by the transport.
func validHeaderFieldValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' || c == ' ' || (c >= 0x21 && c <= 0x7e) {
			continue
		}
		return false
	}
	return true
}

// encodeHeader serializes a header parameter: only style "simple" is allowed.
// There is no escaping defined for header parameters, so any value containing
// a structural character that would change the decoded result is rejected.
func encodeHeader(out *EncodedParameter, param *openapi3.Parameter, sm *openapi3.SerializationMethod,
	kind valueKind, values []string, props [][2]string,
) error {
	if sm.Style != openapi3.SerializationSimple {
		return unsupportedSerializationMethodError(param, sm)
	}
	if param.Name == "" {
		return fmt.Errorf("a header parameter requires a name")
	}
	out.HeaderName = http.CanonicalHeaderKey(param.Name)

	switch kind {
	case kindPrimitive:
		if !validHeaderFieldValue(values[0]) {
			return fmt.Errorf("the value contains bytes that are not legal in an HTTP header field and header parameters define no escaping")
		}
		out.HeaderValue = values[0]
	case kindArray:
		if len(values) == 0 {
			return errEmptyComposite(sm.Style)
		}
		for i, v := range values {
			if err := forbidAny(fmt.Sprintf("array item %d", i), v, ","); err != nil {
				return err
			}
			if strings.TrimSpace(v) != v {
				return fmt.Errorf("array item %d has leading or trailing whitespace, which is stripped when a simple header array is parsed", i)
			}
		}
		joined := strings.Join(values, ",")
		if !validHeaderFieldValue(joined) {
			return fmt.Errorf("the value contains bytes that are not legal in an HTTP header field and header parameters define no escaping")
		}
		out.HeaderValue = joined
	case kindObject:
		if len(props) == 0 {
			return errEmptyComposite(sm.Style)
		}
		valueSep := ","
		if sm.Explode {
			valueSep = "="
		}
		for _, kv := range props {
			if err := forbidAny("object property name", kv[0], ",="); err != nil {
				return err
			}
			if err := forbidAny(fmt.Sprintf("object property %q value", kv[0]), kv[1], ","+valueSep); err != nil {
				return err
			}
			if strings.TrimSpace(kv[1]) != kv[1] {
				return fmt.Errorf("object property %q value has leading or trailing whitespace, which is stripped when a simple header object is parsed", kv[0])
			}
		}
		joined := joinObject(props, sm.Explode, ",")
		if !validHeaderFieldValue(joined) {
			return fmt.Errorf("the value contains bytes that are not legal in an HTTP header field and header parameters define no escaping")
		}
		out.HeaderValue = joined
	}
	return nil
}

// cookie-octet per RFC 6265: US-ASCII excluding CTLs, whitespace, DQUOTE,
// comma, semicolon and backslash. Go's http.Request.AddCookie silently drops
// bytes outside this set, so they are rejected here to avoid information loss.
const forbiddenCookieOctets = " \t\",;\x00\n\r\\"

func validCookieName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f || strings.IndexByte(forbiddenCookieOctets, c) >= 0 {
			return false
		}
	}
	return true
}

func validCookieValue(s string) error {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e || c == '"' || c == ';' || c == '\\' {
			return fmt.Errorf("cookie value contains byte %q which is not a cookie-octet (RFC 6265) and cookie parameters define no escaping", c)
		}
	}
	return nil
}

// encodeCookie serializes a cookie parameter using style "form". Arrays and
// objects use the non-explode comma form; explode arrays/objects and other
// styles are not allowed. Cookie values are not percent-escaped, so values
// containing a comma in a composite value cannot be represented.
func encodeCookie(out *EncodedParameter, param *openapi3.Parameter, sm *openapi3.SerializationMethod,
	kind valueKind, values []string, props [][2]string,
) error {
	if sm.Style != openapi3.SerializationForm {
		return unsupportedSerializationMethodError(param, sm)
	}
	if !validCookieName(param.Name) {
		return fmt.Errorf("cookie name %q is not a valid RFC 6265 cookie name", param.Name)
	}
	out.CookieName = param.Name

	switch kind {
	case kindPrimitive:
		if err := validCookieValue(values[0]); err != nil {
			return err
		}
		out.CookieValue = values[0]
	case kindArray:
		if sm.Explode {
			return unsupportedSerializationMethodError(param, sm)
		}
		if len(values) == 0 {
			return errEmptyComposite(sm.Style)
		}
		for i, v := range values {
			if err := forbidAny(fmt.Sprintf("array item %d", i), v, ","); err != nil {
				return err
			}
			if err := validCookieValue(v); err != nil {
				return err
			}
		}
		out.CookieValue = strings.Join(values, ",")
	case kindObject:
		if sm.Explode {
			return unsupportedSerializationMethodError(param, sm)
		}
		if len(props) == 0 {
			return errEmptyComposite(sm.Style)
		}
		for _, kv := range props {
			if err := forbidAny("object property name", kv[0], ","); err != nil {
				return err
			}
			if err := forbidAny(fmt.Sprintf("object property %q value", kv[0]), kv[1], ","); err != nil {
				return err
			}
			if err := validCookieValue(kv[0]); err != nil {
				return err
			}
			if err := validCookieValue(kv[1]); err != nil {
				return err
			}
		}
		out.CookieValue = joinObject(props, false, ",")
	}
	return nil
}
