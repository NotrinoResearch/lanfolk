//go:build !windows

package winnet

import "os/exec"

func hideWindow(*exec.Cmd) {}

func powershellExe() string { return "powershell.exe" }
