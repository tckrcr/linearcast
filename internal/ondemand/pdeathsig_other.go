//go:build !linux

package ondemand

import "os/exec"

func setProcessDeathSignal(cmd *exec.Cmd) {}
