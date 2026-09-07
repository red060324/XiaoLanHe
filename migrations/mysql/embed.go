package mysql

import "embed"

// Files is the independent, ordered MySQL migration namespace.
//
//go:embed *.sql
var Files embed.FS
