package main

import (
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

// A tiny Multicast DNS responder (RFC 6762) so the deck is reachable at http://<name>.local:8045
// (like umbrel.local) instead of an IP address that changes with DHCP. It answers only "A" queries
// for our own name, needs no Avahi/Bonjour service on the computer, and works the same on Windows.
//
// Client support: iPhone/iPad (any iOS), macOS, Windows 10+, Linux with Avahi and Android 12+ resolve
// .local names. Older Android doesn't, so the IP link is still printed as a fallback.

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

type mdnsResponder struct {
	fqdn string        // "oldphonedeck.local."
	ip   func() net.IP // current LAN address (re-read on every answer: DHCP may change it)
	conn *net.UDPConn
}

// startMDNS begins answering for host + ".local". Errors are returned so main can warn and carry on.
func startMDNS(host string, ip func() net.IP) error {
	// ListenMulticastUDP sets SO_REUSEADDR, so this coexists with Avahi/Bonjour/adb on port 5353.
	conn, err := net.ListenMulticastUDP("udp4", lanInterface(ip()), mdnsGroup)
	if err != nil {
		return err
	}
	p := ipv4.NewPacketConn(conn)
	_ = p.SetMulticastTTL(255) // RFC 6762 §11
	_ = p.SetMulticastLoopback(true)

	r := &mdnsResponder{fqdn: strings.ToLower(host) + ".local.", ip: ip, conn: conn}
	go r.serve()
	go func() { // announce, so caches that saw an old address update right away
		for i := 0; i < 2; i++ {
			r.send(r.answer(0, nil), mdnsGroup)
			time.Sleep(time.Second)
		}
	}()
	return nil
}

func (r *mdnsResponder) serve() {
	buf := make([]byte, 9000)
	for {
		n, src, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			logf("mDNS stopped: %v", err)
			return
		}
		var p dnsmessage.Parser
		hdr, err := p.Start(buf[:n])
		if err != nil || hdr.Response {
			continue
		}
		qs, err := p.AllQuestions()
		if err != nil {
			continue
		}
		if debugMDNS {
			for _, q := range qs {
				logf("mDNS: %v asks %s %v class=%#x", src, q.Name, q.Type, uint16(q.Class))
			}
		}
		for _, q := range qs {
			if !strings.EqualFold(q.Name.String(), r.fqdn) || (q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeALL) {
				continue
			}
			unicastBit := q.Class&0x8000 != 0
			if src.Port != 5353 {
				// "Legacy unicast" query (e.g. a plain resolver): reply directly, echoing ID and question.
				q.Class &^= 0x8000
				r.send(r.answer(hdr.ID, &q), src)
			} else if unicastBit {
				r.send(r.answer(0, nil), src)
			} else {
				r.send(r.answer(0, nil), mdnsGroup)
			}
			break
		}
	}
}

func (r *mdnsResponder) answer(id uint16, q *dnsmessage.Question) []byte {
	ip := r.ip().To4()
	if ip == nil {
		return nil
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true, Authoritative: true})
	b.EnableCompression()
	if q != nil {
		_ = b.StartQuestions()
		_ = b.Question(*q)
	}
	_ = b.StartAnswers()
	class := dnsmessage.ClassINET
	ttl := uint32(10) // legacy unicast answers SHOULD use a short TTL
	if q == nil {
		class |= 0x8000 // cache-flush: we are the only owner of this name
		ttl = 120
	}
	_ = b.AResource(dnsmessage.ResourceHeader{
		Name: dnsmessage.MustNewName(r.fqdn), Class: class, TTL: ttl,
	}, dnsmessage.AResource{A: [4]byte(ip)})
	msg, err := b.Finish()
	if err != nil {
		return nil
	}
	return msg
}

func (r *mdnsResponder) send(msg []byte, to *net.UDPAddr) {
	if msg != nil {
		_, _ = r.conn.WriteToUDP(msg, to)
	}
}

// lanInterface returns the interface that owns ip, so the multicast group is joined on the Wi-Fi/LAN
// rather than whatever the OS picks (often a Docker or VPN bridge). nil = let the OS choose.
func lanInterface(ip net.IP) *net.Interface {
	ifaces, _ := net.Interfaces()
	for i := range ifaces {
		addrs, _ := ifaces[i].Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return &ifaces[i]
			}
		}
	}
	return nil
}

var debugMDNS = os.Getenv("OPD_DEBUG") != ""
