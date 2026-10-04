// Package link evaluates OpenAPI 3.0 Link Objects into typed input plans for
// the next operation call. It performs no network I/O and never splices
// values into URLs: the returned Plan hands typed parameter values and a
// request body to the caller's encoding layer.
//
// Link values are either literals or OpenAPI 3.0 runtime expressions:
//
//   - $url
//   - $method
//   - $statusCode
//   - $request.path.name, $request.query.name, $request.header.name,
//     $request.body and $request.body#/json/pointer
//   - $response.header.name, $response.statusCode, $response.body and
//     $response.body#/json/pointer
//
// Body fragments are interpreted as JSON Pointers (RFC 6901, including "~1"
// and "~0" escaping). A missing value, an explicit JSON null and an invalid
// expression are reported separately, and invalid expressions carry their
// position inside the Link Object.
package link
