package daemon

import (
	"context"
	"net"
	"strconv"
	"time"
)

func splitHost(addr string) (string, string, error) { return net.SplitHostPort(addr) }

func joinHost(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }

// netDialer sets large socket buffers; helps on high-latency Wi-Fi links.
type netDialer struct{}

func (netDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	c, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetReadBuffer(4 << 20)
		tc.SetWriteBuffer(4 << 20)
	}
	return c, nil
}
