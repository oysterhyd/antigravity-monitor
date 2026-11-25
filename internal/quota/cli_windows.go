package quota

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func cliProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
