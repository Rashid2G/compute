package dns

import (
	"context"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

type mapResolver map[string][]net.IP

func (m mapResolver) ResolveA(_ context.Context, qname string) ([]net.IP, bool) {
	ips, ok := m[qname]
	return ips, ok
}

func queryServer(t *testing.T, srv *Server, qname string, qtype dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	var msg dnsmessage.Message
	msg.Header.ID = 0x1234
	msg.Header.RecursionDesired = true
	msg.Questions = []dnsmessage.Question{{
		Name:  dnsmessage.MustNewName(qname),
		Type:  qtype,
		Class: dnsmessage.ClassINET,
	}}
	query, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	respBytes, err := srv.handleQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(respBytes); err != nil {
		t.Fatal(err)
	}
	return resp
}

func aRecords(t *testing.T, resp dnsmessage.Message) []net.IP {
	t.Helper()
	var out []net.IP
	for _, ans := range resp.Answers {
		if ans.Header.Type != dnsmessage.TypeA {
			t.Fatalf("unexpected answer type %v", ans.Header.Type)
		}
		body, ok := ans.Body.(*dnsmessage.AResource)
		if !ok {
			t.Fatalf("unexpected answer body %T", ans.Body)
		}
		out = append(out, net.IP(body.A[:]))
	}
	return out
}

func TestSanitizeLabel(t *testing.T) {
	cases := []struct {
		in, want string
		max      int
	}{
		{"My VM!", "my-vm", 0},
		{"  --Foo--Bar--  ", "foo-bar", 0},
		{"café", "caf", 0},
		{"wunderbar-sandbox-name-that-is-quite-long", "wunderbar-sandbox-name-that-is-q", 32},
		{"", "", 0},
		{"valid-name", "valid-name", 0},
		{"dots.and.dashes", "dots-and-dashes", 0},
	}
	for _, c := range cases {
		if got := SanitizeLabel(c.in, c.max); got != c.want {
			t.Errorf("SanitizeLabel(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestValidLabel(t *testing.T) {
	for _, ok := range []string{"a", "my-vm", "vm-1", "0x"} {
		if !ValidLabel(ok) {
			t.Errorf("ValidLabel(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "-x", "x-", "My VM", "a.b", "toolong-" + strings.Repeat("x", 70)} {
		if ValidLabel(bad) {
			t.Errorf("ValidLabel(%q) = true, want false", bad)
		}
	}
}

func TestZoneAnswers(t *testing.T) {
	zone := "compute-lab.tail123.ts.net"
	res := mapResolver{
		"myvm.alice." + zone: {net.ParseIP("100.101.102.103"), net.ParseIP("172.16.5.9")},
		"vm.dhcp." + zone:    {net.ParseIP("172.16.9.9")},
	}
	srv := NewServer([]string{zone}, res)

	// Sandbox name with two addresses, in the order provided.
	resp := queryServer(t, srv, "myvm.alice."+zone+".", dnsmessage.TypeA)
	if resp.Header.RCode != dnsmessage.RCodeSuccess || !resp.Header.Authoritative {
		t.Fatalf("unexpected header %+v", resp.Header)
	}
	got := aRecords(t, resp)
	if len(got) != 2 || !got[0].Equal(net.ParseIP("100.101.102.103")) || !got[1].Equal(net.ParseIP("172.16.5.9")) {
		t.Fatalf("answers = %v", got)
	}

	// Unknown sandbox name -> NXDOMAIN.
	resp = queryServer(t, srv, "nope.alice."+zone+".", dnsmessage.TypeA)
	if resp.Header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("want NXDOMAIN, got %v", resp.Header.RCode)
	}

	// Reserved apex name (host itself) -> NXDOMAIN when resolver says unknown.
	resp = queryServer(t, srv, "api."+zone+".", dnsmessage.TypeA)
	if resp.Header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("want NXDOMAIN for api., got %v", resp.Header.RCode)
	}

	// AAAA -> NODATA (success, no answers).
	resp = queryServer(t, srv, "myvm.alice."+zone+".", dnsmessage.TypeAAAA)
	if resp.Header.RCode != dnsmessage.RCodeSuccess || len(resp.Answers) != 0 {
		t.Fatalf("AAAA: want NODATA, got rcode=%v answers=%d", resp.Header.RCode, len(resp.Answers))
	}
}

func TestMultiLabelZonesLongestFirst(t *testing.T) {
	long := "sandbox.example.com"
	short := "example.com"
	// The short zone resolver would also see long-zone names; the server must
	// route by the longest matching zone, so queries for the long zone reach
	// the long zone's data in the resolver.
	res := mapResolver{
		"myvm.alice." + long: {net.ParseIP("10.1.2.3")},
		"myvm.bob." + short:  {net.ParseIP("10.9.9.9")},
	}
	srv := NewServer([]string{short, long}, res)
	if z := srv.Zones(); len(z) != 2 || z[0] != long {
		t.Fatalf("zones not sorted longest-first: %v", z)
	}
	if got := aRecords(t, queryServer(t, srv, "myvm.alice."+long+".", dnsmessage.TypeA)); len(got) != 1 || !got[0].Equal(net.ParseIP("10.1.2.3")) {
		t.Fatalf("long zone answers = %v", got)
	}
	if got := aRecords(t, queryServer(t, srv, "myvm.bob.example.com.", dnsmessage.TypeA)); len(got) != 1 || !got[0].Equal(net.ParseIP("10.9.9.9")) {
		t.Fatalf("short zone answers = %v", got)
	}
}

func TestUDPEndToEnd(t *testing.T) {
	zone := "boxes.internal"
	res := mapResolver{
		"web.alice." + zone: {net.ParseIP("172.16.1.2")},
	}
	srv := NewServer([]string{zone}, res)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() { done <- srv.ServeUDP(conn) }()

	client, err := net.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var msg dnsmessage.Message
	msg.Header.ID = 7
	msg.Questions = []dnsmessage.Question{{
		Name:  dnsmessage.MustNewName("web.alice." + zone + "."),
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}}
	q, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(q); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(timeNowAdd())
	buf := make([]byte, 512)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:n]); err != nil {
		t.Fatal(err)
	}
	ips := aRecords(t, resp)
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("172.16.1.2")) {
		t.Fatalf("udp answers = %v", ips)
	}

	srv.Shutdown()
	if err := <-done; err != nil {
		t.Fatalf("ServeUDP returned %v", err)
	}
}

func TestSplitZoneLabelsAndSandboxNames(t *testing.T) {
	zone := "lab.example.com"
	labels, ok := SplitZoneLabels("myvm.alice."+zone, zone)
	if !ok {
		t.Fatal("expected in-zone")
	}
	sandbox, user, ok := ParseSandboxName(labels)
	if !ok || sandbox != "myvm" || user != "alice" {
		t.Fatalf("got %q/%q ok=%v", sandbox, user, ok)
	}

	// Reserved host labels are not sandbox names.
	labels, ok = SplitZoneLabels("api."+zone, zone)
	if !ok {
		t.Fatal("expected in-zone")
	}
	if _, _, ok := ParseSandboxName(labels); ok {
		t.Fatal("api. should not parse as a sandbox name")
	}
	if !IsReservedServiceLabel("API") {
		t.Fatal("API should be a reserved label")
	}

	// Apex.
	if labels, ok := SplitZoneLabels(zone, zone); !ok || labels != nil {
		t.Fatalf("apex: labels=%v ok=%v", labels, ok)
	}

	// Outside zone.
	if _, ok := SplitZoneLabels("foo.other.com", zone); ok {
		t.Fatal("expected out-of-zone")
	}
}
