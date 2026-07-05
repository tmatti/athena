// Package migrations embeds the goose SQL migrations so the binary is
// self-contained.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
