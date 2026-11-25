//go:build !monitor_sqlite_cgo

package source

import _ "modernc.org/sqlite"

const sqliteDriver = "sqlite"
