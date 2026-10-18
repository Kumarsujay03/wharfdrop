package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Kumarsujay03/wharfdrop/internal/daemon"
	"rsc.io/qr"
)

// phoneCmd: wharfdrop phone [stop|list]
func phoneCmd(dir string, args []string) {
	c := start(dir)
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "stop":
		c.post("/api/phone/stop", nil, nil)
		fmt.Println("Phone link closed.")
	case "list":
		s := c.mustState()
		if len(s.Phone.Phones) == 0 {
			fmt.Println("No phones yet. Run: wharfdrop phone")
		}
		for _, p := range s.Phone.Phones {
			st := "not connected"
			if p.Online {
				st = "connected"
			}
			rem := ""
			if p.Remembered {
				rem = " · remembered"
			}
			fmt.Printf("  %-22s %s%s\n", p.Name, st, rem)
		}
	case "":
		var out struct {
			URL     string `json:"url"`
			Options []struct {
				URL, Label string
			} `json:"options"`
			Firewall *struct {
				Text, Command string
			} `json:"firewall"`
		}
		c.post("/api/phone/start", nil, &out)
		fmt.Println("Scan this with your phone's camera (same Wi-Fi as this computer):")
		fmt.Println()
		printQR(out.URL)
		fmt.Println()
		fmt.Println("Or open this address on the phone:")
		fmt.Println("  " + out.URL)
		if len(out.Options) > 1 {
			fmt.Println("\nIf the phone cannot connect, this computer has other addresses (use the same #key):")
			for _, o := range out.Options[1:] {
				fmt.Printf("  %s\n", o.Label)
			}
		}
		if out.Firewall != nil {
			fmt.Println("\nIf the phone stays on a blank page: " + out.Firewall.Text)
			if out.Firewall.Command != "" {
				fmt.Println("  " + out.Firewall.Command)
			}
		}
		fmt.Println("\nThe phone link closes after 10 minutes without activity. Close it now with: wharfdrop phone stop")
	default:
		die("usage: wharfdrop phone [stop|list]")
	}
}

// printQR draws a QR code with half-block characters, light modules in the
// terminal's foreground colour, which scans well on dark and light themes
// because the code is drawn inverted with a quiet zone.
func printQR(text string) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		fmt.Println("(could not draw QR code)")
		return
	}
	const quiet = 2
	n := code.Size
	light := func(x, y int) bool {
		if x < 0 || y < 0 || x >= n || y >= n {
			return true
		}
		return !code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		b.WriteString("  ")
		for x := -quiet; x < n+quiet; x++ {
			top, bot := light(x, y), light(x, y+1)
			switch {
			case top && bot:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bot:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	fmt.Print(b.String())
}

// phoneByName finds a phone by name (case-insensitive).
func phoneByName(s *daemon.State, name string) (daemon.PhoneView, bool) {
	for _, p := range s.Phone.Phones {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return daemon.PhoneView{}, false
}

func sendToPhone(c *client, p daemon.PhoneView, paths []string) {
	if !p.Online {
		die(p.Name + " is not connected. Open the phone link with: wharfdrop phone")
	}
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		check(err)
		if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
			die(path + ": only files can be sent to phones")
		}
		c.post("/api/phone/send-path", map[string]string{"phone": p.ID, "path": abs}, nil)
		fmt.Printf("  %s offered to %s. Tap Save on the phone to keep it.\n", filepath.Base(abs), p.Name)
	}
}
