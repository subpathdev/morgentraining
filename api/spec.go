// Package apispec embeds the OpenAPI description, the source of truth for the HTTP API.
// The server code in internal/api is generated from it (see internal/api/generate.go).
package apispec

import _ "embed"

//go:embed openapi.yaml
var YAML []byte
