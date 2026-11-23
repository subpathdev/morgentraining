// Package seed embeds the starting plans and people, loaded into an empty database on first start.
package seed

import "embed"

// Files holds the plan and people files; plans are imported before people.
//
//go:embed *.yaml
var Files embed.FS
