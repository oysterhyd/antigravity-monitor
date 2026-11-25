//go:build !windows

package quota

import "syscall"

func cliProcessAttributes() *syscall.SysProcAttr {
	return nil
}
