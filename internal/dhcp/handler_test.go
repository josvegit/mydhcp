package dhcp_test

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/josvegit/mydhcp/internal/dhcp"
	"github.com/josvegit/mydhcp/internal/lease"
	"github.com/josvegit/mydhcp/internal/plugin"
	"github.com/josvegit/mydhcp/internal/store"
	"github.com/josvegit/mydhcp/internal/subnet"
	"github.com/josvegit/mydhcp/internal/ztp"
)

var (
	testServerIP = net.ParseIP("192.168.10.1").To4()
	testMAC      = net.HardwareAddr{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01}
)

func testSubnetConfig() subnet.Config {
	_, network, _ := net.ParseCIDR("192.168.10.0/24")
	return subnet.Config{
		Name:            "test",
		Network:         network,
		Router:          net.ParseIP("192.168.10.1").To4(),
		DNS:             []net.IP{net.ParseIP("8.8.8.8").To4()},
		BroadcastAddr:   net.ParseIP("192.168.10.255").To4(),
		LeaseTime:       24 * time.Hour,
		OfferTimeout:    30 * time.Second,
		DeclineCooldown: 10 * time.Minute,
		RangeStart:      net.ParseIP("192.168.10.100").To4(),
		RangeEnd:        net.ParseIP("192.168.10.150").To4(),
	}
}

func testStore() *store.MemoryStore {
	return store.NewMemoryStore(
		net.ParseIP("192.168.10.100").To4(),
		net.ParseIP("192.168.10.150").To4(),
		24*time.Hour, 30*time.Second, 10*time.Minute,
	)
}

func tinyStore() *store.MemoryStore {
	return store.NewMemoryStore(
		net.ParseIP("192.168.10.100").To4(),
		net.ParseIP("192.168.10.100").To4(),
		24*time.Hour, 30*time.Second, 10*time.Minute,
	)
}

func testHandler() *dhcp.Handler {
	return dhcp.NewHandler(testServerIP, plugin.NewRegistry())
}

func makeDiscover(xid uint32, mac net.HardwareAddr) *dhcp.Packet {
	return &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   xid,
		Flags: dhcp.BroadcastFlag,
		CHAddr: mac,
		Options: dhcp.Options{
			dhcp.OptMsgType: {dhcp.MsgDiscover},
		},
	}
}

func makeRequest(xid uint32, mac net.HardwareAddr, serverIP, requestedIP net.IP) *dhcp.Packet {
	return &dhcp.Packet{
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
}

func dora(t *testing.T, h *dhcp.Handler, cfg subnet.Config, st *store.MemoryStore, mac net.HardwareAddr, xid uint32) net.IP {
	t.Helper()

	offer := h.Dispatch(makeDiscover(xid, mac), cfg, st, nil, nil)
	if offer == nil {
		t.Fatal("DISCOVER: expected OFFER, got nil")
	}
	mt, _ := offer.Options.MsgType()
	if mt != dhcp.MsgOffer {
		t.Fatalf("DISCOVER: expected OFFER(2), got msg type %d", mt)
	}

	ack := h.Dispatch(makeRequest(xid, mac, testServerIP, offer.YIAddr), cfg, st, nil, nil)
	if ack == nil {
		t.Fatal("REQUEST: expected ACK, got nil")
	}
	mt, _ = ack.Options.MsgType()
	if mt != dhcp.MsgAck {
		t.Fatalf("REQUEST: expected ACK(5), got msg type %d", mt)
	}

	return ack.YIAddr
}

// --- Tests ---

func TestDORA_FullFlow(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	offer := h.Dispatch(makeDiscover(0xdeadbeef, testMAC), cfg, st, nil, nil)
	if offer == nil {
		t.Fatal("expected OFFER, got nil")
	}
	mt, ok := offer.Options.MsgType()
	if !ok || mt != dhcp.MsgOffer {
		t.Fatalf("expected OFFER(2), got %d", mt)
	}
	if offer.YIAddr == nil || offer.YIAddr.IsUnspecified() {
		t.Fatal("OFFER has no YIAddr")
	}
	if !cfg.Network.Contains(offer.YIAddr) {
		t.Errorf("offered IP %s outside subnet %s", offer.YIAddr, cfg.Network)
	}
	if svrID := offer.Options.ServerID(); !svrID.Equal(testServerIP) {
		t.Errorf("wrong server ID in OFFER: got %s, want %s", svrID, testServerIP)
	}

	offeredIP := offer.YIAddr
	ack := h.Dispatch(makeRequest(0xdeadbeef, testMAC, testServerIP, offeredIP), cfg, st, nil, nil)
	if ack == nil {
		t.Fatal("expected ACK, got nil")
	}
	mt, _ = ack.Options.MsgType()
	if mt != dhcp.MsgAck {
		t.Fatalf("expected ACK(5), got %d", mt)
	}
	if !ack.YIAddr.Equal(offeredIP) {
		t.Errorf("ACK IP %s != offered IP %s", ack.YIAddr, offeredIP)
	}

	l, ok := st.Get(offeredIP)
	if !ok {
		t.Fatal("lease not found in store after ACK")
	}
	if l.State != lease.StateBound {
		t.Errorf("lease state = %s, want bound", l.State)
	}
}

func TestDORA_ThenRelease(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	ip := dora(t, h, cfg, st, testMAC, 0x1)

	rel := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   0x1,
		CHAddr: testMAC,
		CIAddr: ip,
		Options: dhcp.Options{
			dhcp.OptMsgType:  {dhcp.MsgRelease},
			dhcp.OptServerID: dhcp.EncodeIP(testServerIP),
		},
	}
	reply := h.Dispatch(rel, cfg, st, nil, nil)
	if reply != nil {
		t.Error("RELEASE should return nil reply")
	}

	if _, ok := st.Get(ip); ok {
		t.Error("lease still in store after RELEASE")
	}
}

func TestRequest_IgnoredOnWrongServerID(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	offer := h.Dispatch(makeDiscover(0x1234, testMAC), cfg, st, nil, nil)
	if offer == nil {
		t.Fatal("expected OFFER")
	}

	wrongServer := net.ParseIP("192.168.10.99").To4()
	req := makeRequest(0x1234, testMAC, wrongServer, offer.YIAddr)
	reply := h.Dispatch(req, cfg, st, nil, nil)
	if reply != nil {
		mt, _ := reply.Options.MsgType()
		t.Errorf("expected nil (RFC 2131 ignore), got msg type %d", mt)
	}
}

func TestRequest_NakWhenIPNotOffered(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	req := makeRequest(0xabcd, testMAC, testServerIP, net.ParseIP("192.168.10.100").To4())
	reply := h.Dispatch(req, cfg, st, nil, nil)
	if reply == nil {
		t.Fatal("expected NAK, got nil")
	}
	mt, _ := reply.Options.MsgType()
	if mt != dhcp.MsgNak {
		t.Errorf("expected NAK(6), got %d", mt)
	}
}

func TestDecline_MarksIPDeclined(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	offer := h.Dispatch(makeDiscover(0xbbbb, testMAC), cfg, st, nil, nil)
	if offer == nil {
		t.Fatal("expected OFFER")
	}

	dec := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   0xbbbb,
		CHAddr: testMAC,
		Options: dhcp.Options{
			dhcp.OptMsgType:     {dhcp.MsgDecline},
			dhcp.OptRequestedIP: dhcp.EncodeIP(offer.YIAddr),
		},
	}
	reply := h.Dispatch(dec, cfg, st, nil, nil)
	if reply != nil {
		t.Error("DECLINE should produce no reply")
	}

	l, ok := st.Get(offer.YIAddr)
	if !ok {
		t.Fatal("lease should still exist after DECLINE")
	}
	if l.State != lease.StateDeclined {
		t.Errorf("expected declined state, got %s", l.State)
	}
}

func TestInform_AckNoYIAddr(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	inf := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   0xcccc,
		CIAddr: net.ParseIP("192.168.10.200").To4(),
		CHAddr: testMAC,
		Options: dhcp.Options{
			dhcp.OptMsgType: {dhcp.MsgInform},
		},
	}
	reply := h.Dispatch(inf, cfg, st, nil, nil)
	if reply == nil {
		t.Fatal("INFORM expects ACK reply")
	}
	mt, _ := reply.Options.MsgType()
	if mt != dhcp.MsgAck {
		t.Errorf("expected ACK(5), got %d", mt)
	}
	if reply.YIAddr != nil && !reply.YIAddr.Equal(net.IPv4zero) {
		t.Errorf("INFORM ACK must not assign IP, got YIAddr=%s", reply.YIAddr)
	}
}

func TestDiscover_PoolExhausted(t *testing.T) {
	_, network, _ := net.ParseCIDR("192.168.10.0/24")
	cfg := subnet.Config{
		Name:            "tiny",
		Network:         network,
		Router:          net.ParseIP("192.168.10.1").To4(),
		LeaseTime:       24 * time.Hour,
		OfferTimeout:    30 * time.Second,
		DeclineCooldown: 10 * time.Minute,
		RangeStart:      net.ParseIP("192.168.10.100").To4(),
		RangeEnd:        net.ParseIP("192.168.10.100").To4(),
	}
	st := tinyStore()
	h := testHandler()

	mac1 := net.HardwareAddr{0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	mac2 := net.HardwareAddr{0x22, 0x22, 0x22, 0x22, 0x22, 0x22}

	if offer := h.Dispatch(makeDiscover(0x1, mac1), cfg, st, nil, nil); offer == nil {
		t.Fatal("first DISCOVER should get OFFER")
	}

	if offer := h.Dispatch(makeDiscover(0x2, mac2), cfg, st, nil, nil); offer != nil {
		t.Error("second DISCOVER on exhausted pool should return nil")
	}
}

func TestDORA_StaticIP(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	staticIP := net.ParseIP("192.168.10.100").To4()
	mac, _ := net.ParseMAC("de:ad:be:ef:00:02")
	device := &ztp.DeviceRecord{
		MAC:      mac,
		StaticIP: staticIP,
	}

	offer := h.Dispatch(makeDiscover(0xaaaa, mac), cfg, st, device, nil)
	if offer == nil {
		t.Fatal("expected OFFER")
	}
	if !offer.YIAddr.Equal(staticIP) {
		t.Errorf("offered %s, want static %s", offer.YIAddr, staticIP)
	}

	ack := h.Dispatch(makeRequest(0xaaaa, mac, testServerIP, staticIP), cfg, st, device, nil)
	if ack == nil {
		t.Fatal("expected ACK")
	}
	mt, _ := ack.Options.MsgType()
	if mt != dhcp.MsgAck {
		t.Errorf("expected ACK(5), got %d", mt)
	}
	if !ack.YIAddr.Equal(staticIP) {
		t.Errorf("ACK IP %s != static %s", ack.YIAddr, staticIP)
	}
}

func TestDORA_ZTPOptions(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	device := &ztp.DeviceRecord{MAC: mac}
	profile := &ztp.VendorProfile{Name: "testprofile"}

	offer := h.Dispatch(makeDiscover(0xffff, mac), cfg, st, device, profile)
	if offer == nil {
		t.Fatal("expected OFFER")
	}
	if _, ok := offer.Options[dhcp.OptTFTPServer]; !ok {
		t.Error("OFFER should contain TFTP server option (66) when profile is set")
	}
	if _, ok := offer.Options[dhcp.OptBootfile]; !ok {
		t.Error("OFFER should contain Bootfile option (67) when profile is set")
	}
}

func TestDORA_MultipleClients(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	const n = 10
	var wg sync.WaitGroup
	ips := make([]net.IP, n)
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mac := net.HardwareAddr{0xca, 0xfe, 0x00, 0x00, 0x00, byte(i)}
			ip := dora(t, h, cfg, st, mac, uint32(0x1000+i))
			if ip == nil {
				errs[i] = fmt.Errorf("client %d: got nil IP", i)
				return
			}
			ips[i] = ip
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("client %d: %v", i, err)
		}
	}

	seen := make(map[string]int)
	for i, ip := range ips {
		if ip == nil {
			continue
		}
		key := ip.String()
		if prev, dup := seen[key]; dup {
			t.Errorf("clients %d and %d got the same IP %s", prev, i, key)
		}
		seen[key] = i
	}

	if bound := st.OccupiedCount(); bound != n {
		t.Errorf("expected %d occupied leases, got %d", n, bound)
	}
}

func TestDORA_ReuseAfterRelease(t *testing.T) {
	h := testHandler()
	_, network, _ := net.ParseCIDR("192.168.10.0/24")
	cfg := subnet.Config{
		Name:            "single",
		Network:         network,
		Router:          net.ParseIP("192.168.10.1").To4(),
		LeaseTime:       24 * time.Hour,
		OfferTimeout:    30 * time.Second,
		DeclineCooldown: 10 * time.Minute,
		RangeStart:      net.ParseIP("192.168.10.100").To4(),
		RangeEnd:        net.ParseIP("192.168.10.100").To4(),
	}
	st := tinyStore()

	macA := net.HardwareAddr{0xaa, 0x00, 0x00, 0x00, 0x00, 0x01}
	macB := net.HardwareAddr{0xbb, 0x00, 0x00, 0x00, 0x00, 0x02}

	ipA := dora(t, h, cfg, st, macA, 0x1)

	rel := &dhcp.Packet{
		Op:    dhcp.OpRequest,
		HType: dhcp.HTypeEthernet,
		HLen:  dhcp.HLenEthernet,
		XID:   0x1,
		CHAddr: macA,
		CIAddr: ipA,
		Options: dhcp.Options{
			dhcp.OptMsgType:  {dhcp.MsgRelease},
			dhcp.OptServerID: dhcp.EncodeIP(testServerIP),
		},
	}
	h.Dispatch(rel, cfg, st, nil, nil)

	offer := h.Dispatch(makeDiscover(0x2, macB), cfg, st, nil, nil)
	if offer == nil {
		t.Fatal("client B should get OFFER after A released")
	}
	if !offer.YIAddr.Equal(ipA) {
		t.Errorf("client B got %s, expected reuse of %s", offer.YIAddr, ipA)
	}
}

func TestDORA_PreferExistingLease(t *testing.T) {
	h := testHandler()
	cfg := testSubnetConfig()
	st := testStore()

	offer1 := h.Dispatch(makeDiscover(0x1, testMAC), cfg, st, nil, nil)
	if offer1 == nil {
		t.Fatal("first DISCOVER: expected OFFER")
	}

	offer2 := h.Dispatch(makeDiscover(0x2, testMAC), cfg, st, nil, nil)
	if offer2 == nil {
		t.Fatal("second DISCOVER: expected OFFER")
	}
	if !offer2.YIAddr.Equal(offer1.YIAddr) {
		t.Errorf("second DISCOVER offered %s, want same IP %s", offer2.YIAddr, offer1.YIAddr)
	}
}
