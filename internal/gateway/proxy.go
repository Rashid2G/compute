package gateway

import (
	"io"
	"net"
	"sync"
	"time"
)

// tcpProxy accepts connections on a local ephemeral port and forwards them to
// a sandbox guest address. tailscaled is configured to dial the local port.
type tcpProxy struct {
	listener net.Listener
	guest    string
	port     int

	closeOnce sync.Once
}

func startProxy(guest, bindHost string) (*tcpProxy, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil && bindHost != "" {
		// Fall back to a wildcard listener if the preferred address is not
		// assignable in this namespace.
		ln, err = net.Listen("tcp", ":0")
	}
	if err != nil {
		return nil, err
	}
	p := &tcpProxy{listener: ln, guest: guest, port: ln.Addr().(*net.TCPAddr).Port}
	go p.acceptLoop()
	return p, nil
}

func (p *tcpProxy) acceptLoop() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *tcpProxy) handle(src net.Conn) {
	defer src.Close()
	dst, err := net.DialTimeout("tcp", p.guest, 10*time.Second)
	if err != nil {
		return
	}
	defer dst.Close()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(src, dst)
		if tc, ok := src.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
}

func (p *tcpProxy) close() {
	p.closeOnce.Do(func() { _ = p.listener.Close() })
}
