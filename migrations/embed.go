// Package migrations embeds the ordered SQL schema migrations.
//
// Files are named NNNN_description.sql and applied in lexical order by
// internal/store. A migration, once shipped, is never edited: add a new one.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
