// Package web embeds the static front end served by the single binary.
package web

import "embed"

//go:embed static
var FS embed.FS
