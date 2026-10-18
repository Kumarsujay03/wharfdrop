package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// FirewallHint describes how to let phones reach the phone port when a
// firewall on this computer is likely to block it. Opening a port needs admin
// rights, so Wharfdrop shows the command instead of running it.
type FirewallHint struct {
	Name    string `json:"name"`
	Text    string `json:"text"`
	Command string `json:"command,omitempty"`
}

func firewallHint(port int) *FirewallHint {
	switch runtime.GOOS {
	case "linux":
		if b, err := os.ReadFile("/etc/ufw/ufw.conf"); err == nil && strings.Contains(string(b), "ENABLED=yes") {
			return &FirewallHint{Name: "Ubuntu firewall (ufw)",
				Text:    "The firewall on this computer blocks phones until you allow the phone port once. Run this in a terminal:",
				Command: fmt.Sprintf("sudo ufw allow %d/tcp", port)}
		}
		if out, err := exec.Command("systemctl", "is-active", "firewalld").Output(); err == nil && strings.TrimSpace(string(out)) == "active" {
			return &FirewallHint{Name: "firewalld",
				Text:    "The firewall on this computer blocks phones until you allow the phone port once. Run this in a terminal:",
				Command: fmt.Sprintf("sudo firewall-cmd --permanent --add-port=%d/tcp && sudo firewall-cmd --reload", port)}
		}
	case "windows":
		return &FirewallHint{Name: "Windows Defender Firewall",
			Text:    "If Windows asked about Wharfdrop, it must be allowed on Private networks, and your Wi-Fi must be set to Private. Or run this once in an administrator PowerShell:",
			Command: fmt.Sprintf(`New-NetFirewallRule -DisplayName "Wharfdrop phone link" -Direction Inbound -Protocol TCP -LocalPort %d -Action Allow -Profile Private`, port)}
	case "darwin":
		return &FirewallHint{Name: "macOS firewall",
			Text: "If macOS asks whether Wharfdrop may accept incoming connections, choose Allow."}
	}
	return nil
}
