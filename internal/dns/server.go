package dns

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Resolver answers queries for names inside the configured zones. Implementations
// return the addresses for a name, or ok=false when the name is unknown.
type Resolver interface {
	// ResolveA returns the IPv4 (and optionally IPv6-preferred-first) answers
	// for an A query on qname. qname is lowercase, without a trailing dot, and
	// guaranteed to be inside one of the server's zones. ok=false yields a
	// NXDOMAIN response.
	ResolveA(ctx context.Context, qname string) ([]net.IP, bool)
}

// Server is a minimal authoritative DNS server for the configured base
// domains. Queries inside a zone are answered by the Resolver; everything
// else is forwarded to the system resolver so the daemon can also be used as
// a catch-all nameserver (e.g. as a Tailscale global nameserver).
type Server struct {
	zones   []string // lowercase, no trailing dot, longest first
	res     Resolver
	forward *net.Resolver

	ttl    uint32
	mu     sync.RWMutex
	udp    net.PacketConn
	tcp    net.Listener
	closed bool
}

// NewServer builds a DNS server serving the given base domains. Zones are
// lowercased and sorted longest-first so multi-label zones win over shorter
// suffixes.
func NewServer(zones []string, res Resolver) *Server {
	cleaned := make([]string, 0, len(zones))
	for _, z := range zones {
		z = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(z), "."))
		if z == "" {
			continue
		}
		cleaned = append(cleaned, z)
	}
	// Longest suffix first.
	for i := 1; i < len(cleaned); i++ {
		for j := i; j > 0 && len(cleaned[j]) > len(cleaned[j-1]); j-- {
			cleaned[j], cleaned[j-1] = cleaned[j-1], cleaned[j]
		}
	}
	return &Server{
		zones:   cleaned,
		res:     res,
		forward: net.DefaultResolver,
		ttl:     15,
	}
}

// Zones returns the configured base domains (longest first).
func (s *Server) Zones() []string {
	out := make([]string, len(s.zones))
	copy(out, s.zones)
	return out
}

// ListenAndServeUDP serves DNS over UDP on addr (e.g. ":53"). It blocks until
// the connection is closed.
func (s *Server) ListenAndServeUDP(addr string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("dns: udp listen %s: %w", addr, err)
	}
	return s.ServeUDP(conn)
}

// ServeUDP serves DNS over an existing UDP packet connection.
func (s *Server) ServeUDP(conn net.PacketConn) error {
	s.mu.Lock()
	s.udp = conn
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		conn.Close()
	}()

	buf := make([]byte, 512)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if s.isClosed() {
				return nil
			}
			return fmt.Errorf("dns: udp read: %w", err)
		}
		query := make([]byte, n)
		copy(query, buf[:n])
		go func(a net.Addr) {
			resp, err := s.handleQuery(query)
			if err != nil {
				log.Printf("[dns] query error from %s: %v", a, err)
				return
			}
			if _, err := conn.WriteTo(resp, a); err != nil {
				log.Printf("[dns] udp write to %s: %v", a, err)
			}
		}(addr)
	}
}

// ListenAndServeTCP serves DNS over TCP on addr (clients fall back to TCP).
func (s *Server) ListenAndServeTCP(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("dns: tcp listen %s: %w", addr, err)
	}
	return s.ServeTCP(l)
}

// ServeTCP serves DNS over an existing TCP listener.
func (s *Server) ServeTCP(l net.Listener) error {
	s.mu.Lock()
	s.tcp = l
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		l.Close()
	}()

	for {
		c, err := l.Accept()
		if err != nil {
			if s.isClosed() {
				return nil
			}
			return fmt.Errorf("dns: tcp accept: %w", err)
		}
		go s.serveTCPConn(c)
	}
}

func (s *Server) serveTCPConn(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	var lenBuf [2]byte
	for {
		if _, err := readFull(c, lenBuf[:]); err != nil {
			return
		}
		qlen := int(lenBuf[0])<<8 | int(lenBuf[1])
		if qlen == 0 || qlen > 65535 {
			return
		}
		query := make([]byte, qlen)
		if _, err := readFull(c, query); err != nil {
			return
		}
		resp, err := s.handleQuery(query)
		if err != nil {
			return
		}
		if len(resp) > 65535 {
			return
		}
		out := make([]byte, 2+len(resp))
		out[0] = byte(len(resp) >> 8)
		out[1] = byte(len(resp))
		copy(out[2:], resp)
		if _, err := c.Write(out); err != nil {
			return
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
	}
}

func readFull(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (s *Server) isClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// Shutdown stops both listeners.
func (s *Server) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.udp != nil {
		s.udp.Close()
	}
	if s.tcp != nil {
		s.tcp.Close()
	}
}

// handleQuery parses one DNS message and produces the response bytes.
func (s *Server) handleQuery(query []byte) ([]byte, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(query); err != nil {
		return nil, fmt.Errorf("unpack: %w", err)
	}
	if !msg.Response && len(msg.Questions) == 1 {
		q := msg.Questions[0]
		qname := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))

		switch {
		case q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeAAAA:
			// Only address records are answered authoritatively.
			return s.respond(msg, q, qname, nil, false, dnsmessage.RCodeSuccess)
		case s.zoneFor(qname) != "":
			ips, ok := s.resolveZone(qname)
			if !ok {
				return s.respond(msg, q, qname, nil, false, dnsmessage.RCodeNameError)
			}
			return s.respond(msg, q, qname, ips, true, dnsmessage.RCodeSuccess)
		default:
			ips, ok := s.forwardLookup(qname)
			if !ok {
				return s.respond(msg, q, qname, nil, false, dnsmessage.RCodeNameError)
			}
			return s.respond(msg, q, qname, ips, false, dnsmessage.RCodeSuccess)
		}
	}
	return nil, errors.New("dns: unsupported query shape")
}

// zoneFor returns the matching base domain for qname ("" when outside all zones).
func (s *Server) zoneFor(qname string) string {
	for _, z := range s.zones {
		if qname == z || strings.HasSuffix(qname, "."+z) {
			return z
		}
	}
	return ""
}

func (s *Server) resolveZone(qname string) ([]net.IP, bool) {
	if s.res == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, ok := s.res.ResolveA(ctx, qname)
	if !ok || len(ips) == 0 {
		return nil, false
	}
	return ips, true
}

func (s *Server) forwardLookup(qname string) ([]net.IP, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ips, err := s.forward.LookupNetIP(ctx, "ip", qname)
	if err != nil || len(ips) == 0 {
		return nil, false
	}
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if ip.Is4() {
			a := ip.As4()
			out = append(out, net.IPv4(a[0], a[1], a[2], a[3]))
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// respond serializes an A-record answer for q. Only IPv4 answers are emitted;
// AAAA queries for zone names get an empty (NODATA) success response.
func (s *Server) respond(msg dnsmessage.Message, q dnsmessage.Question, qname string, ips []net.IP, authoritative bool, rcode dnsmessage.RCode) ([]byte, error) {
	wantAAAA := q.Type == dnsmessage.TypeAAAA
	header := dnsmessage.Header{
		ID:                 msg.Header.ID,
		Response:           true,
		OpCode:             msg.Header.OpCode,
		Authoritative:      authoritative,
		RecursionDesired:   msg.Header.RecursionDesired,
		RecursionAvailable: !authoritative,
		RCode:              rcode,
	}
	b := dnsmessage.NewBuilder(nil, header)
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	if rcode == dnsmessage.RCodeSuccess {
		var v4 []net.IP
		for _, ip := range ips {
			if a := ip.To4(); a != nil {
				v4 = append(v4, a)
			}
		}
		if err := b.StartAnswers(); err != nil {
			return nil, err
		}
		if !wantAAAA {
			name, err := dnsmessage.NewName(qname + ".")
			if err != nil {
				return nil, err
			}
			for _, ip := range v4 {
				var a [4]byte
				copy(a[:], ip.To4())
				if err := b.AResource(dnsmessage.ResourceHeader{
					Name:  name,
					Type:  dnsmessage.TypeA,
					Class: dnsmessage.ClassINET,
					TTL:   s.ttl,
				}, dnsmessage.AResource{A: a}); err != nil {
					return nil, err
				}
			}
		}
		// AAAA queries intentionally fall through with zero answers: NODATA.
	}
	if err := b.StartAuthorities(); err != nil {
		return nil, err
	}
	if err := b.StartAdditionals(); err != nil {
		return nil, err
	}
	out, err := b.Finish()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SplitZoneLabels splits qname into its labels and returns the prefix labels
// that precede zone ("" when qname is not inside zone).
func SplitZoneLabels(qname, zone string) ([]string, bool) {
	qname = strings.ToLower(strings.TrimSuffix(qname, "."))
	zone = strings.ToLower(strings.TrimSuffix(zone, "."))
	if qname == zone {
		return nil, true
	}
	if !strings.HasSuffix(qname, "."+zone) {
		return nil, false
	}
	prefix := strings.TrimSuffix(qname, "."+zone)
	labels := strings.Split(prefix, ".")
	if len(labels) == 1 && labels[0] == "" {
		return nil, true // apex: qname == zone handled above, defensive
	}
	return labels, true
}

// ParseSandboxName interprets the labels preceding a zone as
// "<sandbox>.<username>" and returns them, or ok=false for any other shape
// (apex, reserved service labels, or deeper nesting).
func ParseSandboxName(labels []string) (sandbox, username string, ok bool) {
	if len(labels) == 2 && labels[0] != "" && labels[1] != "" {
		return labels[0], labels[1], true
	}
	return "", "", false
}

// ReservedServiceLabels are zone-root labels that belong to the host itself
// (GUI, API, www) and never resolve to a sandbox.
var ReservedServiceLabels = map[string]bool{
	"api": true,
	"gui": true,
	"www": true,
}

// IsReservedServiceLabel reports whether the single prefix label is one of the
// reserved host-service labels.
func IsReservedServiceLabel(label string) bool {
	return ReservedServiceLabels[strings.ToLower(label)]
}
