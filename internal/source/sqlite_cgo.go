//go:build monitor_sqlite_cgo && cgo

package source

import _ "github.com/mattn/go-sqlite3"

const sqliteDriver = "sqlite3"
