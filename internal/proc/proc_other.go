//go:build !unix

package proc

import (
	"os/exec"
	"time"
)

func configureCancel(cmd *exec.Cmd) {
	// CommandContext already kills the process when ctx is done.
	// WaitDelay reaps grandchildren that ignore that signal.
	cmd.WaitDelay = 5 * time.Second
}
