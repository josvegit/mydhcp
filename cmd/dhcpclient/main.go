package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"time"

	"github.com/josvegit/mydhcp/internal/dhcp"
)

func main() {
	server := flag.String("server", "172.28.0.2", "DHCP server IP")
	macStr := flag.String("mac", "", "Client MAC address (random if not set)")
	vendorClass := flag.String("vendor", "", "Vendor class string (option 60)")
	reqIPStr := flag.String("reqip", "", "Request specific IP (option 50)")
	xidVal := flag.Uint("xid", 0, "Transaction ID (random if not set)")
	timeout := flag.Duration("timeout", 5*time.Second, "Per-step timeout")
	scenario := flag.String("scenario", "dora", "Scenario: dora|discover-only|bad-server-id|bad-ip|decline|release|renew|flood")
	floodN := flag.Int("flood-count", 20, "Number of clients for flood scenario")
	flag.Parse()

	mac, err := parseMACOrRandom(*macStr)
	if err != nil {
		log.Fatalf("invalid -mac: %v", err)
	}

	xid := uint32(*xidVal)
	if xid == 0 {
		xid = rand.Uint32()
	}

	serverIP := net.ParseIP(*server).To4()
	if serverIP == nil {
		log.Fatalf("invalid -server: %q", *server)
	}

	conn, err := listenUDP68()
	if err != nil {
		log.Fatalf("listen UDP/68: %v (are you running as root / inside container?)", err)
	}
	defer conn.Close()

	c := &client{
		conn:       conn,
		serverAddr: &net.UDPAddr{IP: serverIP, Port: 67},
		mac:        mac,
		xid:        xid,
		timeout:    *timeout,
	}

	var reqIP net.IP
	if *reqIPStr != "" {
		reqIP = net.ParseIP(*reqIPStr).To4()
		if reqIP == nil {
			log.Fatalf("invalid -reqip: %q", *reqIPStr)
		}
	}

	switch *scenario {
	case "dora":
		runDORA(c, serverIP, reqIP, *vendorClass)
	case "discover-only":
		runDiscoverOnly(c, reqIP, *vendorClass)
	case "bad-server-id":
		runBadServerID(c, serverIP, *vendorClass)
	case "bad-ip":
		runBadIP(c, serverIP, *vendorClass)
	case "decline":
		runDecline(c, serverIP, *vendorClass)
	case "release":
		runRelease(c, serverIP, *vendorClass)
	case "renew":
		runRenew(c, serverIP, *vendorClass)
	case "flood":
		runFlood(conn, serverIP, *floodN, *timeout)
	default:
		log.Fatalf("unknown scenario %q. Valid: dora|discover-only|bad-server-id|bad-ip|decline|release|renew|flood", *scenario)
	}
}

// --- Scenario runners ---

func runDORA(c *client, serverIP, reqIP net.IP, vendor string) {
	offer := c.mustDiscover(reqIP, vendor)
	ack := c.mustRequest(offer.YIAddr, serverIP)
	logf("✓ bound  ip=%-16s  lease=%s", ack.YIAddr, leaseTime(ack))
}

func runDiscoverOnly(c *client, reqIP net.IP, vendor string) {
	offer := c.mustDiscover(reqIP, vendor)
	logf("✓ offer received — NOT sending REQUEST (offer will expire)")
	logf("  offered ip=%s", offer.YIAddr)
}

func runBadServerID(c *client, _ net.IP, vendor string) {
	offer := c.mustDiscover(nil, vendor)
	fakeServer := net.ParseIP("192.0.2.1").To4()
	logf("→ DHCPREQUEST  xid=0x%08x  requested=%s  server=%s (WRONG)", c.xid, offer.YIAddr, fakeServer)
	req := buildRequest(c.mac, c.xid, fakeServer, offer.YIAddr, vendor)
	if err := c.send(req); err != nil {
		log.Fatalf("send REQUEST: %v", err)
	}
	reply, err := c.recv(c.xid)
	if err != nil {
		logf("✓ no reply (server ignored REQUEST with wrong server ID, as expected)")
		return
	}
	mt, _ := reply.Options.MsgType()
	logf("  unexpected reply: msg_type=%d", mt)
}

func runBadIP(c *client, serverIP net.IP, vendor string) {
	offer := c.mustDiscover(nil, vendor)
	badIP := bumpIP(offer.YIAddr, 100)
	logf("→ DHCPREQUEST  xid=0x%08x  requested=%s (different from offered %s)", c.xid, badIP, offer.YIAddr)
	req := buildRequest(c.mac, c.xid, serverIP, badIP, vendor)
	if err := c.send(req); err != nil {
		log.Fatalf("send REQUEST: %v", err)
	}
	reply, err := c.recv(c.xid)
	if err != nil {
		logf("✗ no reply (timeout)")
		return
	}
	printPacket("←", reply)
}

func runDecline(c *client, serverIP net.IP, vendor string) {
	offer := c.mustDiscover(nil, vendor)
	ack := c.mustRequest(offer.YIAddr, serverIP)
	logf("✓ bound  ip=%s", ack.YIAddr)

	dec := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   c.xid,
		Flags: dhcp.BroadcastFlag,
		CHAddr: c.mac,
		Options: dhcp.Options{
			dhcp.OptMsgType:     {dhcp.MsgDecline},
			dhcp.OptServerID:    dhcp.EncodeIP(serverIP),
			dhcp.OptRequestedIP: dhcp.EncodeIP(ack.YIAddr),
		},
	}
	logf("→ DHCPDECLINE  xid=0x%08x  ip=%s  (simulating ARP conflict)", c.xid, ack.YIAddr)
	if err := c.send(dec); err != nil {
		log.Fatalf("send DECLINE: %v", err)
	}
	logf("✓ decline sent — server marks ip=%s as declined (cooldown applies)", ack.YIAddr)
}

func runRelease(c *client, serverIP net.IP, vendor string) {
	offer := c.mustDiscover(nil, vendor)
	ack := c.mustRequest(offer.YIAddr, serverIP)
	logf("✓ bound  ip=%s", ack.YIAddr)

	rel := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   c.xid,
		CHAddr: c.mac,
		CIAddr: ack.YIAddr,
		Options: dhcp.Options{
			dhcp.OptMsgType:  {dhcp.MsgRelease},
			dhcp.OptServerID: dhcp.EncodeIP(serverIP),
		},
	}
	logf("→ DHCPRELEASE  xid=0x%08x  ip=%s", c.xid, ack.YIAddr)
	if err := c.send(rel); err != nil {
		log.Fatalf("send RELEASE: %v", err)
	}
	logf("✓ released — ip=%s is available again", ack.YIAddr)
}

func runRenew(c *client, serverIP net.IP, vendor string) {
	offer := c.mustDiscover(nil, vendor)
	ack := c.mustRequest(offer.YIAddr, serverIP)
	logf("✓ bound  ip=%s", ack.YIAddr)

	// Renewal: CIAddr set, no OptRequestedIP, no OptServerID (unicast renew)
	renew := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   rand.Uint32(),
		Flags: dhcp.BroadcastFlag,
		CIAddr: ack.YIAddr,
		CHAddr: c.mac,
		Options: dhcp.Options{
			dhcp.OptMsgType:  {dhcp.MsgRequest},
			dhcp.OptServerID: dhcp.EncodeIP(serverIP),
		},
	}
	logf("→ DHCPREQUEST (renew)  ciaddr=%s", ack.YIAddr)
	if err := c.send(renew); err != nil {
		log.Fatalf("send renew REQUEST: %v", err)
	}
	reply, err := c.recv(renew.XID)
	if err != nil {
		log.Fatalf("renew: no reply: %v", err)
	}
	printPacket("←", reply)
}

func runFlood(conn *net.UDPConn, serverIP net.IP, n int, timeout time.Duration) {
	logf("flooding %d DORA sequences concurrently...", n)

	d := newDispatcher(conn)
	var wg sync.WaitGroup
	results := make(chan string, n)

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mac := randomMAC()
			xid := rand.Uint32()
			serverAddr := &net.UDPAddr{IP: serverIP, Port: 67}

			fc := &floodClient{dispatcher: d, serverAddr: serverAddr, mac: mac, xid: xid, timeout: timeout}
			ip, err := fc.dora()
			if err != nil {
				results <- fmt.Sprintf("client[%02d] %s FAIL: %v", i, net.HardwareAddr(mac), err)
				return
			}
			results <- fmt.Sprintf("client[%02d] %s → %s", i, net.HardwareAddr(mac), ip)
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	ok, fail := 0, 0
	for r := range results {
		logf("  %s", r)
		if len(r) > 4 && r[len(r)-4:] == "FAIL" {
			fail++
		} else {
			ok++
		}
	}
	logf("flood done: %d succeeded, %d failed", ok, fail)
}

// --- client ---

type client struct {
	conn       *net.UDPConn
	serverAddr *net.UDPAddr
	mac        net.HardwareAddr
	xid        uint32
	timeout    time.Duration
}

func (c *client) mustDiscover(reqIP net.IP, vendor string) *dhcp.Packet {
	pkt := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   c.xid,
		Flags: dhcp.BroadcastFlag,
		CHAddr: c.mac,
		Options: dhcp.Options{
			dhcp.OptMsgType: {dhcp.MsgDiscover},
		},
	}
	if vendor != "" {
		pkt.Options[dhcp.OptVendorClass] = []byte(vendor)
	}
	if reqIP != nil {
		pkt.Options[dhcp.OptRequestedIP] = dhcp.EncodeIP(reqIP)
	}

	logf("→ DHCPDISCOVER  xid=0x%08x  mac=%s", c.xid, c.mac)
	if err := c.send(pkt); err != nil {
		log.Fatalf("send DISCOVER: %v", err)
	}

	offer, err := c.recv(c.xid)
	if err != nil {
		log.Fatalf("DISCOVER: no OFFER received: %v", err)
	}
	printPacket("←", offer)
	return offer
}

func (c *client) mustRequest(offeredIP, serverIP net.IP) *dhcp.Packet {
	req := buildRequest(c.mac, c.xid, serverIP, offeredIP, "")
	logf("→ DHCPREQUEST  xid=0x%08x  requested=%s  server=%s", c.xid, offeredIP, serverIP)
	if err := c.send(req); err != nil {
		log.Fatalf("send REQUEST: %v", err)
	}
	reply, err := c.recv(c.xid)
	if err != nil {
		log.Fatalf("REQUEST: no reply received: %v", err)
	}
	printPacket("←", reply)

	mt, _ := reply.Options.MsgType()
	if mt == dhcp.MsgNak {
		log.Fatalf("got NAK — server refused the request")
	}
	return reply
}

func (c *client) send(pkt *dhcp.Packet) error {
	_, err := c.conn.WriteToUDP(pkt.Serialize(), c.serverAddr)
	return err
}

func (c *client) recv(xid uint32) (*dhcp.Packet, error) {
	deadline := time.Now().Add(c.timeout)
	buf := make([]byte, 65536)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(deadline)
		n, _, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		pkt, err := dhcp.Parse(buf[:n])
		if err != nil {
			continue
		}
		if pkt.Op != dhcp.OpReply || pkt.XID != xid {
			continue
		}
		return pkt, nil
	}
	return nil, fmt.Errorf("timeout after %s", c.timeout)
}

// --- flood dispatcher (shared conn, routes by XID) ---

type dispatcher struct {
	conn    *net.UDPConn
	mu      sync.Mutex
	waiters map[uint32]chan *dhcp.Packet
}

func newDispatcher(conn *net.UDPConn) *dispatcher {
	d := &dispatcher{conn: conn, waiters: make(map[uint32]chan *dhcp.Packet)}
	go d.readLoop()
	return d
}

func (d *dispatcher) readLoop() {
	buf := make([]byte, 65536)
	for {
		n, _, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		pkt, err := dhcp.Parse(buf[:n])
		if err != nil || pkt.Op != dhcp.OpReply {
			continue
		}
		d.mu.Lock()
		ch, ok := d.waiters[pkt.XID]
		d.mu.Unlock()
		if ok {
			select {
			case ch <- pkt:
			default:
			}
		}
	}
}

func (d *dispatcher) subscribe(xid uint32) chan *dhcp.Packet {
	ch := make(chan *dhcp.Packet, 4)
	d.mu.Lock()
	d.waiters[xid] = ch
	d.mu.Unlock()
	return ch
}

func (d *dispatcher) unsubscribe(xid uint32) {
	d.mu.Lock()
	delete(d.waiters, xid)
	d.mu.Unlock()
}

type floodClient struct {
	dispatcher *dispatcher
	serverAddr *net.UDPAddr
	mac        net.HardwareAddr
	xid        uint32
	timeout    time.Duration
}

func (fc *floodClient) dora() (net.IP, error) {
	ch := fc.dispatcher.subscribe(fc.xid)
	defer fc.dispatcher.unsubscribe(fc.xid)

	disc := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   fc.xid,
		Flags: dhcp.BroadcastFlag,
		CHAddr: fc.mac,
		Options: dhcp.Options{
			dhcp.OptMsgType: {dhcp.MsgDiscover},
		},
	}
	if _, err := fc.dispatcher.conn.WriteToUDP(disc.Serialize(), fc.serverAddr); err != nil {
		return nil, fmt.Errorf("send DISCOVER: %w", err)
	}

	offer, err := recvFromChan(ch, fc.xid, dhcp.MsgOffer, fc.timeout)
	if err != nil {
		return nil, fmt.Errorf("no OFFER: %w", err)
	}

	svrID := offer.Options.ServerID()
	req := buildRequest(fc.mac, fc.xid, svrID, offer.YIAddr, "")
	if _, err := fc.dispatcher.conn.WriteToUDP(req.Serialize(), fc.serverAddr); err != nil {
		return nil, fmt.Errorf("send REQUEST: %w", err)
	}

	ack, err := recvFromChan(ch, fc.xid, dhcp.MsgAck, fc.timeout)
	if err != nil {
		return nil, fmt.Errorf("no ACK: %w", err)
	}
	return ack.YIAddr, nil
}

func recvFromChan(ch chan *dhcp.Packet, xid uint32, wantType byte, timeout time.Duration) (*dhcp.Packet, error) {
	deadline := time.After(timeout)
	for {
		select {
		case pkt := <-ch:
			if pkt.XID != xid {
				continue
			}
			mt, ok := pkt.Options.MsgType()
			if !ok {
				continue
			}
			if mt == wantType {
				return pkt, nil
			}
			if mt == dhcp.MsgNak {
				return nil, fmt.Errorf("got NAK")
			}
		case <-deadline:
			return nil, fmt.Errorf("timeout")
		}
	}
}

// --- helpers ---

func buildRequest(mac net.HardwareAddr, xid uint32, serverIP, requestedIP net.IP, vendor string) *dhcp.Packet {
	pkt := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   xid,
		Flags: dhcp.BroadcastFlag,
		CHAddr: mac,
		Options: dhcp.Options{
			dhcp.OptMsgType:     {dhcp.MsgRequest},
			dhcp.OptServerID:    dhcp.EncodeIP(serverIP),
			dhcp.OptRequestedIP: dhcp.EncodeIP(requestedIP),
		},
	}
	if vendor != "" {
		pkt.Options[dhcp.OptVendorClass] = []byte(vendor)
	}
	return pkt
}

func listenUDP68() (*net.UDPConn, error) {
	return net.ListenUDP("udp4", &net.UDPAddr{Port: 68})
}

func parseMACOrRandom(s string) (net.HardwareAddr, error) {
	if s == "" {
		return randomMAC(), nil
	}
	return net.ParseMAC(s)
}

func randomMAC() net.HardwareAddr {
	b := make([]byte, 6)
	binary.BigEndian.PutUint32(b[2:], rand.Uint32())
	binary.BigEndian.PutUint16(b[:2], uint16(rand.Uint32()))
	b[0] &^= 0x01 // unicast
	b[0] |= 0x02  // locally administered
	return net.HardwareAddr(b)
}

func bumpIP(ip net.IP, delta int) net.IP {
	out := make(net.IP, 4)
	copy(out, ip.To4())
	out[3] = byte(int(out[3]) + delta)
	return out
}

func leaseTime(pkt *dhcp.Packet) time.Duration {
	v, ok := pkt.Options[dhcp.OptLeaseTime]
	if !ok || len(v) < 4 {
		return 0
	}
	secs := binary.BigEndian.Uint32(v)
	return time.Duration(secs) * time.Second
}

func printPacket(dir string, pkt *dhcp.Packet) {
	mt, _ := pkt.Options.MsgType()
	name := msgTypeName(mt)
	switch mt {
	case dhcp.MsgOffer, dhcp.MsgAck:
		logf("%s %-14s xid=0x%08x  yiaddr=%-16s  server=%s  lease=%s",
			dir, name, pkt.XID, pkt.YIAddr, pkt.Options.ServerID(), leaseTime(pkt))
	case dhcp.MsgNak:
		logf("%s %-14s xid=0x%08x  (refused)", dir, name, pkt.XID)
	default:
		logf("%s %-14s xid=0x%08x", dir, name, pkt.XID)
	}
}

func msgTypeName(t byte) string {
	switch t {
	case dhcp.MsgDiscover:
		return "DHCPDISCOVER"
	case dhcp.MsgOffer:
		return "DHCPOFFER"
	case dhcp.MsgRequest:
		return "DHCPREQUEST"
	case dhcp.MsgAck:
		return "DHCPACK"
	case dhcp.MsgNak:
		return "DHCPNAK"
	case dhcp.MsgRelease:
		return "DHCPRELEASE"
	case dhcp.MsgDecline:
		return "DHCPDECLINE"
	case dhcp.MsgInform:
		return "DHCPINFORM"
	default:
		return fmt.Sprintf("DHCP(%d)", t)
	}
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}
