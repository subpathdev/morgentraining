// Package apispec embeds the OpenAPI description, the source of truth for the HTTP API.
// internal/httpapi/oapi is generated from it (see its generate.go).
package apispec

import _ "embed"

//go:embed openapi.yaml
var YAML []byte
