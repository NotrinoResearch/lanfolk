//go:build windows

package winnet

import (
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// powershellExe returns the absolute path of Windows PowerShell. The service
// runs as LocalSystem, so it must not resolve the program through PATH.
func powershellExe() string {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		dir = `C:\Windows\System32`
	}
	return filepath.Join(dir, "WindowsPowerShell", "v1.0", "powershell.exe")
}
