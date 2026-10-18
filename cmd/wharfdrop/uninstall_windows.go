//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// windowsCleanup removes the Start menu shortcut and the PATH entry now, and
// deletes the running .exe (and its folder, if empty) after we exit.
func windowsCleanup(exe string) {
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		os.Remove(filepath.Join(appdata, "Microsoft", "Windows", "Start Menu", "Programs", "Wharfdrop.lnk"))
	}
	if exe == "" {
		return
	}
	folder := filepath.Dir(exe)

	// Remove our folder from the user PATH (runs now, so errors are visible).
	lit := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	ps := "$d=" + lit(strings.TrimRight(folder, `\`)) + "; " +
		"$p=[Environment]::GetEnvironmentVariable('Path','User'); " +
		"if($p){ $n=(($p -split ';') | Where-Object { $_ -and ($_.TrimEnd([char]92) -ne $d) }) -join ';'; " +
		"if($n -ne $p){ [Environment]::SetEnvironmentVariable('Path', $n, 'User') } }"
	if out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Could not update PATH: %v %s\n", err, strings.TrimSpace(string(out)))
	}

	// A running .exe cannot delete itself; a small cmd.exe helper does it
	// once we have exited. The command line is passed verbatim.
	script := fmt.Sprintf(`for /l %%i in (1,1,40) do (if exist "%s" (del /f /q "%s" >nul 2>&1 & ping -n 2 127.0.0.1 >nul)) & del /f /q "%s.old" >nul 2>&1 & rmdir "%s" >nul 2>&1`,
		exe, exe, exe, folder)
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe /d /c "` + script + `"`,
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Delete %s yourself: %v\n", exe, err)
	}
}
