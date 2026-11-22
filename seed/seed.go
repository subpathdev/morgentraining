// Package seed embeds the starting plans and people, loaded into an empty database on first start.
package seed

import "embed"

// Files holds plan-*.yaml (imported first) and people.yaml.
//
//go:embed *.yaml
var Files embed.FS
