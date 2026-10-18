package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
	"github.com/Kumarsujay03/wharfdrop/internal/update"
)

// uninstall removes Wharfdrop's program file, settings, keys and logs, and on
// Windows its Start menu shortcut and PATH entry. Received files are kept.
func uninstall(dir string, assumeYes bool) {
	cfg, _ := config.Load(dir)
	incoming := ""
	if cfg != nil {
		incoming = cfg.IncomingDir
	}
	exe, _ := update.Executable()

	fmt.Println("This removes Wharfdrop from this computer:")
	if exe != "" {
		fmt.Println("  • the program       ", exe)
	}
	fmt.Println("  • settings and keys ", dir)
	if runtime.GOOS == "windows" {
		fmt.Println("  • the Start menu shortcut and the PATH entry")
	}
	if incoming != "" {
		fmt.Println("\nYour received files are kept in", incoming)
	}
	fmt.Println("Paired devices will need to pair again if you reinstall.")
	if !assumeYes {
		fmt.Print("\nUninstall Wharfdrop? [y/N] ")
		ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "y") {
			fmt.Println("Cancelled. Nothing was removed.")
			return
		}
	}

	// Stop the background node so its files can be removed.
	if c, ok := tryCLI(dir); ok {
		c.do("POST", "/api/shutdown?force=1", nil, nil)
		for i := 0; i < 50; i++ {
			if _, ok := tryCLI(dir); !ok {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	if err := removeConfigDir(dir, incoming); err != nil {
		fmt.Fprintln(os.Stderr, "Could not remove some settings:", err)
	} else {
		fmt.Println("Removed settings and keys.")
	}

	if runtime.GOOS == "windows" {
		windowsCleanup(exe)
		fmt.Println("Wharfdrop is uninstalled. The program file is removed in a few seconds.")
		return
	}
	if exe != "" {
		if err := os.Remove(exe); err != nil {
			fmt.Fprintf(os.Stderr, "Could not remove %s: %v\nRemove it yourself, e.g.: sudo rm %q\n", exe, err, exe)
			os.Exit(1)
		}
		fmt.Println("Removed", exe)
	}
	fmt.Println("Wharfdrop is uninstalled.")
}

// removeConfigDir deletes the config directory, but never the received-files
// folder even if the user placed it inside the config directory.
func removeConfigDir(dir, incoming string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	absIn, _ := filepath.Abs(incoming)
	if incoming == "" || !strings.HasPrefix(absIn+string(filepath.Separator), absDir+string(filepath.Separator)) {
		return os.RemoveAll(absDir)
	}
	// incoming lives inside the config dir: remove everything else.
	return filepath.Walk(absDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || p == absDir {
			return err
		}
		if p == absIn || strings.HasPrefix(absIn, p+string(filepath.Separator)) {
			return nil // keep the received folder and its parents
		}
		if strings.HasPrefix(p, absIn+string(filepath.Separator)) {
			return filepath.SkipDir
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		if fi.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
}

// windowsCleanup removes the shortcut and PATH entry, then deletes the running
// .exe (and its folder, if empty) from a short-lived helper after we exit.
func windowsCleanup(exe string) {
	appdata := os.Getenv("APPDATA")
	if appdata != "" {
		os.Remove(filepath.Join(appdata, "Microsoft", "Windows", "Start Menu", "Programs", "Wharfdrop.lnk"))
	}
	if exe == "" {
		return
	}
	folder := filepath.Dir(exe)
	q := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" } // PowerShell literal string
	ps := "$d=" + q(folder) + "; $e=" + q(exe) + "; " +
		"$p=[Environment]::GetEnvironmentVariable('Path','User'); " +
		"if($p){ [Environment]::SetEnvironmentVariable('Path', (($p -split ';') | Where-Object { $_ -and ($_.TrimEnd('\\') -ne $d.TrimEnd('\\')) }) -join ';', 'User') }; " +
		// wait for wharfdrop.exe to exit, then delete it (retry for up to ~20 s)
		"for($i=0; $i -lt 40; $i++){ Start-Sleep -Milliseconds 500; Remove-Item -Force -LiteralPath $e -ErrorAction SilentlyContinue; if(-not (Test-Path -LiteralPath $e)){ break } }; " +
		"Remove-Item -Force -LiteralPath ($e + '.old') -ErrorAction SilentlyContinue; " +
		"if(-not (Get-ChildItem -LiteralPath $d -Force -ErrorAction SilentlyContinue)){ Remove-Item -Force -LiteralPath $d -ErrorAction SilentlyContinue }"
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-Command", ps)
	detach(cmd)
	cmd.Start()
}
