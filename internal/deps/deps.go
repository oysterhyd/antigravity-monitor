// Package deps pins the shared core dependencies in go.mod/go.sum on main
// so the three parallel tracks do not collide on go.mod/go.sum edits.
package deps

import (
	_ "github.com/egoist/mygo"
	_ "github.com/egoist/mygo/plugins/glass"
	_ "github.com/egoist/mygo/ui"
	_ "github.com/egoist/mygo/ui/chart"
	_ "github.com/fsnotify/fsnotify"
	_ "google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"
)
