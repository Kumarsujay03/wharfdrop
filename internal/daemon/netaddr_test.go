package daemon

import (
	"net"
	"sort"
	"testing"
)

func rank(t *testing.T, primary string, cands map[string]string) []string {
	t.Helper()
	type c struct {
		ip    string
		score int
	}
	var list []c
	for ip, iface := range cands {
		s, _ := scoreAddr(net.ParseIP(ip).To4(), iface, primary)
		list = append(list, c{ip, s})
	}
	sort.Slice(list, func(a, b int) bool { return list[a].score > list[b].score })
	var out []string
	for _, x := range list {
		out = append(out, x.ip)
	}
	return out
}

// The bug that sent phones to 192.168.137.1: Windows keeps its mobile
// hotspot adapter up, and it sorted ahead of the Wi-Fi address.
func TestWiFiBeatsWindowsHotspot(t *testing.T) {
	cands := map[string]string{
		"192.168.137.1": "Local Area Connection* 2",
		"192.168.31.24": "Wi-Fi",
		"172.29.112.1":  "vEthernet (WSL)",
		"192.168.56.1":  "VirtualBox Host-Only Network",
	}
	// with a default route through the Wi-Fi
	if got := rank(t, "192.168.31.24", cands)[0]; got != "192.168.31.24" {
		t.Fatalf("picked %s, want the Wi-Fi address", got)
	}
	// even without internet (no default route), Wi-Fi must still win
	if got := rank(t, "", cands)[0]; got != "192.168.31.24" {
		t.Fatalf("offline: picked %s, want the Wi-Fi address", got)
	}
}

func TestDefaultRouteWins(t *testing.T) {
	cands := map[string]string{"10.0.0.5": "Ethernet", "192.168.1.20": "Wi-Fi"}
	if got := rank(t, "10.0.0.5", cands)[0]; got != "10.0.0.5" {
		t.Fatalf("picked %s, want the default-route address", got)
	}
}

func TestHotspotIsLabelled(t *testing.T) {
	_, label := scoreAddr(net.ParseIP("192.168.137.1").To4(), "Local Area Connection* 2", "")
	if label != "Windows mobile hotspot" {
		t.Fatalf("label %q", label)
	}
}
