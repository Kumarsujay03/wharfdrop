package daemon

import (
	"net"
	"regexp"
	"sort"
	"strings"
)

// LANAddr is one of this computer's IPv4 addresses with a guess at how
// likely other devices on the home network can reach it.
type LANAddr struct {
	IP    string `json:"ip"`
	Iface string `json:"iface"`
	Label string `json:"label"`
	score int
}

var virtualIface = regexp.MustCompile(`(?i)vethernet|hyper-v|virtualbox|vbox|vmware|vmnet|docker|^br-|^veth|virbr|wsl|tailscale|zerotier|wireguard|^wg\d|^tun|^tap|vpn|loopback|bluetooth|npcap|local area connection\*`)
var physicalIface = regexp.MustCompile(`(?i)^wi-?fi|wlan|^wl|wireless|^eth|^en|ethernet`)

// outboundIP returns the address the OS would use to reach the internet,
// i.e. the interface with the default route. A UDP "dial" sends no packets.
func outboundIP() string {
	c, err := net.Dial("udp4", "8.8.8.8:53")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

func inNet(ip net.IP, cidr string) bool {
	_, n, _ := net.ParseCIDR(cidr)
	return n.Contains(ip)
}

// scoreAddr ranks an address: the default-route address first, ordinary
// private home-network addresses next, virtual adapters (Windows mobile
// hotspot, Hyper-V, WSL, VirtualBox, Docker, VPNs) and link-local last.
func scoreAddr(ip net.IP, iface, primary string) (int, string) {
	s := 0
	label := iface
	if ip.String() == primary {
		s += 100
	}
	if ip.IsPrivate() {
		s += 20
	}
	switch {
	case inNet(ip, "192.168.137.0/24"):
		s -= 80
		label = "Windows mobile hotspot"
	case inNet(ip, "192.168.56.0/24"):
		s -= 80
		label = "VirtualBox"
	case inNet(ip, "172.17.0.0/16"), inNet(ip, "169.254.0.0/16"), inNet(ip, "100.64.0.0/10"):
		s -= 80
	}
	if virtualIface.MatchString(iface) {
		s -= 60
	}
	if physicalIface.MatchString(iface) {
		s += 5
	}
	if label == "" {
		label = "network"
	}
	return s, label
}

// lanCandidates lists this computer's IPv4 addresses, best first.
func lanCandidates() []LANAddr {
	primary := outboundIP()
	var out []LANAddr
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			sc, label := scoreAddr(ipn.IP.To4(), i.Name, primary)
			out = append(out, LANAddr{IP: ipn.IP.String(), Iface: i.Name, Label: label, score: sc})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].score > out[b].score })
	return out
}

// LocalAddrs lists this machine's IPv4 addresses, most reachable first.
func LocalAddrs() []string {
	var out []string
	for _, c := range lanCandidates() {
		out = append(out, c.IP)
	}
	return out
}

func friendlyLabel(c LANAddr) string {
	l := strings.TrimSpace(c.Label)
	if l == "" {
		return c.IP
	}
	return l + " · " + c.IP
}
