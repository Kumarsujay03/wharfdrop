// Wharfdrop: secure LAN file sharing between your own devices.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Kumarsujay03/wharfdrop/internal/config"
	"github.com/Kumarsujay03/wharfdrop/internal/daemon"
	"github.com/Kumarsujay03/wharfdrop/internal/identity"
	"github.com/Kumarsujay03/wharfdrop/internal/update"
)

const usage = `Wharfdrop %s — secure file sharing between your own devices on the same Wi-Fi/LAN.

Wharfdrop only runs when you start it. When stopped it uses no CPU, memory or network.

On Windows, double-click wharfdrop.exe (or run it with no command) to start it and open
the web UI.

Usage:
  wharfdrop start                    start in the background
  wharfdrop stop [--force]           stop completely (--force: even during a transfer)
  wharfdrop ui                       start if needed and open the web UI in your browser
  wharfdrop serve                    run in this terminal instead (Ctrl+C stops it)
  wharfdrop status                   show this device, paired devices and devices seen on the LAN
  wharfdrop pair <device|ip:port>    start pairing; a 6-digit code appears on both screens
  wharfdrop allow                    approve a pairing request another device sent to this one
  wharfdrop unpair <device>          forget a paired device
  wharfdrop send <device> <path>...  send files or folders to a device's incoming folder
  wharfdrop shares <device>          list folders a device shares with you
  wharfdrop ls <device> <share>[/path]        list a shared folder
  wharfdrop get <device> <share>[/path] [-o dir]   download a file (or a folder, as zip)
  wharfdrop share add <name> <dir> [--to <device>]  share a local folder (read-only)
  wharfdrop share rm <name>
  wharfdrop share ls
  wharfdrop set name <name> | incoming <dir> | auto-update <auto|notify|off>
  wharfdrop update [--check]         install the latest release now (or only check)
  wharfdrop version
  wharfdrop phone [stop|list]        connect a phone: shows a QR code to scan (no app needed)
  wharfdrop uninstall [--yes]        remove Wharfdrop (keeps your received files)

<device> is a device name (as shown by status) or a fingerprint prefix.
`

func main() {
	doubleClicked = launchedByDoubleClick()
	if len(os.Args) < 2 && (doubleClicked || runtime.GOOS == "windows") {
		// Started without a command (double-click, Start menu, or plain
		// `wharfdrop` on Windows): start in the background and open the UI.
		os.Args = append(os.Args, "ui")
	}
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Printf(usage, daemon.Version)
		return
	}
	dir, err := identity.Dir()
	check(err)
	args := os.Args[2:]
	switch os.Args[1] {
	case "serve":
		serve(dir)
	case "version", "--version", "-v":
		fmt.Println("wharfdrop", daemon.Version)
	case "update":
		selfUpdate(dir, len(args) > 0 && args[0] == "--check")
	case "start":
		c := start(dir)
		fmt.Println("Wharfdrop is running. Web UI:", c.uiURL())
		fmt.Println("Stop it with: wharfdrop stop")
	case "stop":
		stop(dir, len(args) > 0 && args[0] == "--force")
	case "phone":
		phoneCmd(dir, args)
	case "uninstall":
		uninstall(dir, len(args) > 0 && (args[0] == "--yes" || args[0] == "-y"))
	case "ui":
		c := start(dir)
		url := c.uiURL()
		fmt.Println("Wharfdrop is running. Opening", url)
		fmt.Println("Stop it from the web page or with: wharfdrop stop")
		if os.Getenv("WHARFDROP_NO_BROWSER") == "" {
			openBrowser(url)
		}
		if doubleClicked {
			time.Sleep(3 * time.Second) // let the user read it before the window closes
		}
	case "status":
		cli(dir).status()
	case "pair":
		need(args, 1, "pair <device|ip:port>")
		cli(dir).pair(args[0])
	case "allow":
		cli(dir).allow()
	case "unpair":
		need(args, 1, "unpair <device>")
		c := cli(dir)
		c.post("/api/unpair", map[string]string{"fp": c.resolve(args[0])}, nil)
		fmt.Println("unpaired")
	case "send":
		need(args, 2, "send <device> <path>...")
		cli(dir).send(args[0], args[1:])
	case "shares":
		need(args, 1, "shares <device>")
		cli(dir).peerShares(args[0])
	case "ls":
		need(args, 2, "ls <device> <share>[/path]")
		cli(dir).ls(args[0], args[1])
	case "get":
		need(args, 2, "get <device> <share>[/path] [-o dir]")
		cli(dir).get(args[0], args[1], flag(args[2:], "-o"))
	case "share":
		cli(dir).share(args)
	case "set":
		need(args, 2, "set name <name> | set incoming <dir>")
		c := cli(dir)
		body := map[string]string{}
		switch args[0] {
		case "name":
			body["name"] = args[1]
		case "incoming":
			abs, _ := filepath.Abs(args[1])
			body["incoming"] = abs
		case "auto-update":
			body["auto_update"] = args[1]
		default:
			die("set name|incoming|auto-update")
		}
		c.post("/api/settings", body, nil)
		fmt.Println("ok")
	default:
		fmt.Printf(usage, daemon.Version)
		os.Exit(2)
	}
}

func serve(dir string) {
	update.CleanupOld()
	d, err := daemon.New(dir)
	check(err)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = d.Run(ctx)
	stop()
	if errors.Is(err, daemon.ErrRestart) {
		check(update.Restart())
		return
	}
	check(err)
}

// selfUpdate updates through the running daemon (so it restarts itself), or
// updates the binary directly when no daemon is running.
func selfUpdate(dir string, checkOnly bool) {
	if c, ok := tryCLI(dir); ok {
		var st daemon.UpdateState
		c.post("/api/update/check", nil, &st)
		printUpdate(st)
		if checkOnly || !st.Available {
			return
		}
		fmt.Printf("Installing %s ...\n", st.Latest)
		c.post("/api/update/install", nil, &st)
		fmt.Println("Updated. Wharfdrop is restarting with the new version.")
		return
	}
	u := update.New(daemon.Version)
	ctx := context.Background()
	latest, err := u.Latest(ctx)
	check(err)
	printUpdate(daemon.UpdateState{Current: daemon.Version, Latest: latest,
		Available: update.Newer(latest, daemon.Version), DevBuild: !update.IsRelease(daemon.Version)})
	if checkOnly || !update.Newer(latest, daemon.Version) {
		return
	}
	exe, err := update.Executable()
	check(err)
	fmt.Printf("Installing %s ...\n", latest)
	check(u.Install(ctx, latest, exe))
	fmt.Println("Updated", exe, "to", latest)
}

func printUpdate(st daemon.UpdateState) {
	switch {
	case st.Error != "":
		fmt.Println("Update check failed:", st.Error)
	case st.DevBuild:
		fmt.Printf("Running development build %s. Latest release: %s.\n", st.Current, st.Latest)
	case st.Available:
		fmt.Printf("Update available: %s -> %s\n", st.Current, st.Latest)
	default:
		fmt.Printf("Up to date (%s).\n", st.Current)
	}
}

// ---------- thin client over the loopback API ----------

type client struct {
	base, token string
	http        *http.Client
}

func cli(dir string) *client {
	c, ok := tryCLI(dir)
	if !ok {
		die("Wharfdrop is not running. Start it with: wharfdrop start")
	}
	return c
}

// start launches `wharfdrop serve` in the background (unless it already
// runs) and waits until it answers.
func start(dir string) *client {
	if c, ok := tryCLI(dir); ok {
		return c
	}
	exe, err := update.Executable()
	check(err)
	logPath := filepath.Join(dir, "wharfdrop.log")
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 5<<20 {
		os.Rename(logPath, logPath+".1")
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	check(err)
	defer logf.Close()
	cmd := exec.Command(exe, "serve")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = append(os.Environ(), "WHARFDROP_DIR="+dir)
	detach(cmd)
	check(cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	for i := 0; i < 100; i++ {
		select {
		case <-exited:
			die("Wharfdrop failed to start; see " + logPath)
		case <-time.After(100 * time.Millisecond):
		}
		if c, ok := tryCLI(dir); ok {
			cmd.Process.Release()
			return c
		}
	}
	die("Wharfdrop did not come up in time; see " + logPath)
	return nil
}

func stop(dir string, force bool) {
	c, ok := tryCLI(dir)
	if !ok {
		fmt.Println("Wharfdrop is not running.")
		return
	}
	path := "/api/shutdown"
	if force {
		path += "?force=1"
	}
	c.post(path, nil, nil)
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, ok := tryCLI(dir); !ok {
			fmt.Println("Wharfdrop stopped.")
			return
		}
	}
	die("Wharfdrop did not stop in time")
}

// tryCLI connects to a running daemon, if there is one.
func tryCLI(dir string) (*client, bool) {
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, false
	}
	tok, err := os.ReadFile(filepath.Join(dir, "ui-token"))
	if err != nil {
		return nil, false
	}
	c := &client{base: fmt.Sprintf("http://127.0.0.1:%d", cfg.UIPort), token: strings.TrimSpace(string(tok)),
		http: &http.Client{}}
	if _, err := c.state(); err != nil {
		return nil, false
	}
	return c, true
}

func (c *client) uiURL() string { return c.base + "/?t=" + c.token }

func (c *client) do(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("X-Token", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("%s", e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *client) post(path string, body, out any) {
	check(c.do("POST", path, body, out))
}

func (c *client) state() (*daemon.State, error) {
	var s daemon.State
	return &s, c.do("GET", "/api/state", nil, &s)
}

func (c *client) mustState() *daemon.State {
	s, err := c.state()
	check(err)
	return s
}

// resolve maps a device name or fingerprint prefix to a fingerprint.
func (c *client) resolve(dev string) string {
	s := c.mustState()
	var hits []string
	for _, p := range s.Peers {
		if strings.EqualFold(p.Name, dev) || strings.HasPrefix(p.FP, strings.ToLower(dev)) {
			hits = append(hits, p.FP)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	if len(hits) > 1 {
		die("ambiguous device name; use a fingerprint prefix (see: wharfdrop status)")
	}
	die("no device named " + dev + " (see: wharfdrop status)")
	return ""
}

func (c *client) status() {
	s := c.mustState()
	fmt.Printf("This device: %s  id %s  %s\n", s.Self.Name, s.Self.Short, s.Self.Version)
	up := "auto-update " + s.Update.Mode
	if s.Update.Available {
		up += ", update " + s.Update.Latest + " available (run: wharfdrop update)"
	}
	fmt.Printf("Updates:     %s\n", up)
	fmt.Printf("Addresses:   %s  (peer port %d)\n", strings.Join(s.Self.Addrs, ", "), s.Self.Port)
	fmt.Printf("Incoming:    %s\nWeb UI:      %s\n\n", s.Incoming, c.uiURL())
	if len(s.Peers) == 0 {
		fmt.Println("No other devices seen. Run `wharfdrop serve` on the other machine.")
	} else {
		fmt.Println("Devices:")
		sort.Slice(s.Peers, func(i, j int) bool { return s.Peers[i].Name < s.Peers[j].Name })
		for _, p := range s.Peers {
			st := "offline"
			if p.Online {
				st = "online "
			}
			tr := "not paired"
			if p.Trusted {
				tr = "paired"
			}
			fmt.Printf("  %-20s %s  %-10s %-22s %s\n", p.Name, st, tr, p.Addr, p.FP[:12])
		}
	}
	if len(s.Shares) > 0 {
		fmt.Println("\nShared folders:")
		for _, sh := range s.Shares {
			fmt.Printf("  %-12s %s  -> %s\n", sh.Name, sh.Path, strings.Join(sh.Peers, ","))
		}
	}
	for _, p := range s.PendingIn {
		if p.Status == "pending" {
			fmt.Printf("\n%s wants to pair. Code: %s   (run: wharfdrop allow)\n", p.PeerName, p.Code)
		}
	}
}

func (c *client) pair(target string) {
	body := map[string]string{}
	if strings.Contains(target, ":") || strings.Count(target, ".") == 3 {
		body["addr"] = target
	} else {
		body["fp"] = c.resolve(target)
	}
	var pr daemon.PairRequest
	c.post("/api/pair", body, &pr)
	fmt.Printf("\nPairing with %s\n\n    code:  %s\n\n", pr.PeerName, pr.Code)
	fmt.Print("Does the other device show the same code? [y/N] ")
	var ans string
	fmt.Scanln(&ans)
	if !strings.HasPrefix(strings.ToLower(ans), "y") {
		c.post("/api/pair/cancel", map[string]string{"id": pr.ID}, nil)
		die("cancelled")
	}
	c.post("/api/pair/confirm", map[string]string{"id": pr.ID}, nil)
	fmt.Println("Waiting for the other device to tap Allow...")
	for {
		time.Sleep(time.Second)
		s := c.mustState()
		for _, p := range s.PendingOut {
			if p.ID == pr.ID {
				switch p.Status {
				case "accepted":
					fmt.Printf("Paired with %s.\n", p.PeerName)
					return
				case "denied", "cancelled":
					die("pairing " + p.Status + " by the other device")
				}
			}
		}
	}
}

func (c *client) allow() {
	s := c.mustState()
	var pend []daemon.PairRequest
	for _, p := range s.PendingIn {
		if p.Status == "pending" {
			pend = append(pend, p)
		}
	}
	if len(pend) == 0 {
		die("no pending pairing requests")
	}
	for _, p := range pend {
		fmt.Printf("\n%s wants to pair.\n\n    code:  %s\n\n", p.PeerName, p.Code)
		fmt.Print("Allow only if the other device shows the same code. Allow? [y/N] ")
		var ans string
		fmt.Scanln(&ans)
		ok := strings.HasPrefix(strings.ToLower(ans), "y")
		c.post("/api/pair/respond", map[string]any{"id": p.ID, "allow": ok}, nil)
		if ok {
			fmt.Println("Paired with", p.PeerName)
		} else {
			fmt.Println("Denied")
		}
	}
}

func (c *client) send(dev string, paths []string) {
	if p, ok := phoneByName(c.mustState(), dev); ok {
		sendToPhone(c, p, paths)
		return
	}
	fp := c.resolve(dev)
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		check(err)
		done := make(chan error, 1)
		var out []daemon.Transfer
		go func() { done <- c.do("POST", "/api/send-path", map[string]string{"peer": fp, "path": abs}, &out) }()
		watch(c, done, "send")
		for _, t := range out {
			report(t)
		}
	}
}

func (c *client) get(dev, remote, outDir string) {
	fp := c.resolve(dev)
	share, p, _ := strings.Cut(remote, "/")
	if outDir == "" {
		outDir, _ = os.Getwd()
	}
	abs, _ := filepath.Abs(outDir)
	done := make(chan error, 1)
	var t daemon.Transfer
	go func() {
		done <- c.do("POST", "/api/peer/download", map[string]string{"peer": fp, "share": share, "path": p, "dest": abs}, &t)
	}()
	watch(c, done, "download")
	report(t)
}

// watch prints live progress of the newest in-flight transfer of a kind until done closes.
func watch(c *client, done chan error, kind string) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			fmt.Print("\r\033[K")
			check(err)
			return
		case <-tick.C:
			s, err := c.state()
			if err != nil {
				continue
			}
			for _, t := range s.Transfers {
				if t.Kind == kind && t.Finished.IsZero() {
					el := time.Since(t.Started).Seconds()
					rate := 0.0
					if el > 0 {
						rate = float64(t.Done) / el
					}
					if t.Size > 0 {
						fmt.Printf("\r\033[K  %s  %5.1f%%  %s / %s  %s/s", t.Name, float64(t.Done)*100/float64(t.Size), human(t.Done), human(t.Size), human(int64(rate)))
					} else {
						fmt.Printf("\r\033[K  %s  %s  %s/s", t.Name, human(t.Done), human(int64(rate)))
					}
					break
				}
			}
		}
	}
}

func report(t daemon.Transfer) {
	d := t.Finished.Sub(t.Started).Seconds()
	rate := 0.0
	if d > 0 {
		rate = float64(t.Done) / d
	}
	where := ""
	if t.Dest != "" {
		where = "  -> " + t.Dest
	}
	fmt.Printf("  %s  %s in %.1fs (%s/s)%s\n", t.Name, human(t.Done), d, human(int64(rate)), where)
}

func (c *client) peerShares(dev string) {
	fp := c.resolve(dev)
	var out []map[string]any
	check(c.do("GET", "/api/peer/shares?peer="+fp, nil, &out))
	if len(out) == 0 {
		fmt.Println("(no folders shared with you)")
	}
	for _, s := range out {
		fmt.Println(s["name"])
	}
}

func (c *client) ls(dev, remote string) {
	fp := c.resolve(dev)
	share, p, _ := strings.Cut(remote, "/")
	var out []daemon.Entry
	check(c.do("GET", fmt.Sprintf("/api/peer/ls?peer=%s&share=%s&path=%s", fp, share, p), nil, &out))
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return out[i].Name < out[j].Name
	})
	for _, e := range out {
		if e.Dir {
			fmt.Printf("  %10s  %s/\n", "", e.Name)
		} else {
			fmt.Printf("  %10s  %s\n", human(e.Size), e.Name)
		}
	}
}

func (c *client) share(args []string) {
	if len(args) == 0 {
		die("share add|rm|ls")
	}
	switch args[0] {
	case "add":
		need(args, 3, "share add <name> <dir> [--to <device>]")
		abs, _ := filepath.Abs(args[2])
		peers := []string{"*"}
		if to := flag(args[3:], "--to"); to != "" {
			peers = []string{c.resolve(to)}
		}
		var out config.Share
		c.post("/api/shares", map[string]any{"name": args[1], "path": abs, "peers": peers}, &out)
		fmt.Printf("sharing %s as %q with %s\n", out.Path, out.Name, strings.Join(peers, ","))
	case "rm":
		need(args, 2, "share rm <name>")
		check(c.do("DELETE", "/api/shares?name="+args[1], nil, nil))
		fmt.Println("removed")
	case "ls":
		s := c.mustState()
		for _, sh := range s.Shares {
			fmt.Printf("  %-12s %s  -> %s\n", sh.Name, sh.Path, strings.Join(sh.Peers, ","))
		}
	default:
		die("share add|rm|ls")
	}
}

// ---------- helpers ----------

func human(n int64) string {
	const u = "KMGT"
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f, i := float64(n)/1024, 0
	for f >= 1024 && i < len(u)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %cB", f, u[i])
}

func flag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func need(args []string, n int, usage string) {
	if len(args) < n {
		die("usage: wharfdrop " + usage)
	}
}

// doubleClicked is true when Wharfdrop owns its console window (started from
// Explorer). The window would close at once, so errors wait for Enter.
var doubleClicked bool

func die(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	if doubleClicked {
		fmt.Fprint(os.Stderr, "\nPress Enter to close this window.")
		fmt.Scanln()
	}
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		die("error: " + err.Error())
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
