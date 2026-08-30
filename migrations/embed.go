package migrations

import "embed"

// Files contains the immutable ordered database migrations used at startup.
//
//go:embed *.sql
var Files embed.FS
