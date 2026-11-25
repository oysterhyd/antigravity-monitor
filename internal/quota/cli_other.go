//go:build !windows

package quota

import (
	"context"
	"syscall"
)

func cliProcessAttributes() *syscall.SysProcAttr {
	return nil
}

func cliCandidates() []string {
	return nil
}

func prepareCLI(context.Context) (string, error) {
	return "", ErrCLISetup
}
