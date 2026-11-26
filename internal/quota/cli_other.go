//go:build !windows

package quota

import (
	"context"
	"os/exec"
	"syscall"
)

func cliProcessAttributes() *syscall.SysProcAttr {
	return nil
}

func prependOrAppendCLI(candidates []string, lookedUp string) []string {
	return append([]string{lookedUp}, candidates...)
}

func execCLICommand(command *exec.Cmd) ([]byte, error) {
	return command.Output()
}

func cliCandidates() []string {
	return nil
}

func prepareCLI(context.Context) (string, error) {
	return "", ErrCLISetup
}
