// Package web embeds the page.
package web

import "embed"

//go:embed index.html manifest.webmanifest icon.svg
var Files embed.FS
