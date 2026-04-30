package relay

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
)

// Interface describes a VLAN interface the relay agent listens on.
type Interface struct {
	Name    string
	AgentIP net.IP
}

// Config is the relay agent configuration.
type Config struct {
	Listen     string
	Upstream   string
	Interfaces []Interface
}

// Relay is a lightweight DHCP relay agent.
// It binds to each configured interface, receives client broadcasts,
// stamps giaddr, and unicasts to the upstream DHCP server.
type Relay struct {
	cfg Config
}

func New(cfg Config) *Relay {
	return &Relay{cfg: cfg}
}

// Run starts the relay agent. Blocks until ctx is cancelled.
func (r *Relay) Run(ctx context.Context) error {
	upstream, err := net.ResolveUDPAddr("udp4", r.cfg.Upstream)
	if err != nil {
		return fmt.Errorf("resolve upstream: %w", err)
	}

	for _, iface := range r.cfg.Interfaces {
		go r.runInterface(ctx, iface, upstream)
	}

	// Also listen for server replies if not already covered by per-interface binds
	<-ctx.Done()
	return nil
}

func (r *Relay) runInterface(ctx context.Context, iface Interface, upstream *net.UDPAddr) {
	listenAddr := &net.UDPAddr{IP: iface.AgentIP, Port: 67}
	conn, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		slog.Error("relay listen error", "interface", iface.Name, "addr", listenAddr, "err", err)
		return
	}
	defer conn.Close()

	slog.Info("relay listening", "interface", iface.Name, "agent_ip", iface.AgentIP)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 65536)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Error("relay read error", "interface", iface.Name, "err", err)
				continue
			}
		}

		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		go r.forwardToServer(pkt, src, iface, upstream)
	}
}

func (r *Relay) forwardToServer(pkt []byte, _ *net.UDPAddr, iface Interface, upstream *net.UDPAddr) {
	if len(pkt) < 236 {
		return
	}

	// op == 1 means client-to-server
	if pkt[0] != 1 {
		return
	}

	// Set giaddr (bytes 24-27) to this interface's agent_ip if not already set
	giaddr := pkt[24:28]
	if isZero(giaddr) {
		agentIP := iface.AgentIP.To4()
		if agentIP != nil {
			copy(giaddr, agentIP)
		}
	}

	// Increment hops
	pkt[3]++

	conn, err := net.DialUDP("udp4", nil, upstream)
	if err != nil {
		slog.Error("relay dial upstream error", "err", err)
		return
	}
	defer conn.Close()

	if _, err := conn.Write(pkt); err != nil {
		slog.Error("relay forward error", "err", err)
		return
	}

	// Read the server reply
	reply := make([]byte, 65536)
	n, err := conn.Read(reply)
	if err != nil {
		slog.Debug("relay: no reply from server", "err", err)
		return
	}
	reply = reply[:n]

	if len(reply) < 236 {
		return
	}

	// op == 2 means server-to-client
	if reply[0] != 2 {
		return
	}

	r.forwardToClient(reply, iface)
}

func (r *Relay) forwardToClient(pkt []byte, iface Interface) {
	flags := binary.BigEndian.Uint16(pkt[10:12])
	broadcastFlag := flags&0x8000 != 0

	var dstIP net.IP
	if broadcastFlag {
		dstIP = net.IPv4bcast
	} else {
		// yiaddr
		yiaddr := make(net.IP, 4)
		copy(yiaddr, pkt[16:20])
		if isZero(yiaddr) {
			dstIP = net.IPv4bcast
		} else {
			dstIP = yiaddr
		}
	}

	dst := &net.UDPAddr{IP: dstIP, Port: 68}
	src := &net.UDPAddr{IP: iface.AgentIP, Port: 67}

	conn, err := net.DialUDP("udp4", src, dst)
	if err != nil {
		slog.Error("relay: dial client error", "dst", dst, "err", err)
		return
	}
	defer conn.Close()

	if _, err := conn.Write(pkt); err != nil {
		slog.Error("relay: send to client error", "err", err)
	}
}

func isZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
