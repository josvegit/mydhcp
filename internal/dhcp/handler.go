package dhcp

import (
	"log/slog"
	"net"
	"time"

	"github.com/josvegit/mydhcp/internal/lease"
	"github.com/josvegit/mydhcp/internal/plugin"
	"github.com/josvegit/mydhcp/internal/subnet"
	"github.com/josvegit/mydhcp/internal/ztp"
)

// Handler processes DHCP messages for a single subnet.
type Handler struct {
	serverIP net.IP
	plugins  *plugin.Registry
}

func NewHandler(serverIP net.IP, plugins *plugin.Registry) *Handler {
	return &Handler{serverIP: serverIP, plugins: plugins}
}

// Dispatch routes a parsed packet to the appropriate handler.
// Returns the reply packet, or nil if no reply should be sent.
func (h *Handler) Dispatch(
	pkt *Packet,
	cfg subnet.Config,
	store lease.Store,
	device *ztp.DeviceRecord,
	profile *ztp.VendorProfile,
) *Packet {
	msgType, ok := pkt.Options.MsgType()
	if !ok {
		slog.Warn("DHCP packet missing message type", "xid", pkt.XID)
		return nil
	}

	switch msgType {
	case MsgDiscover:
		return h.handleDiscover(pkt, cfg, store, device, profile)
	case MsgRequest:
		return h.handleRequest(pkt, cfg, store, device, profile)
	case MsgRelease:
		h.handleRelease(pkt, cfg, store)
		return nil
	case MsgDecline:
		h.handleDecline(pkt, cfg, store)
		return nil
	case MsgInform:
		return h.handleInform(pkt, cfg)
	default:
		slog.Debug("ignoring DHCP message type", "type", msgType)
		return nil
	}
}

func (h *Handler) handleDiscover(
	pkt *Packet,
	cfg subnet.Config,
	store lease.Store,
	device *ztp.DeviceRecord,
	profile *ztp.VendorProfile,
) *Packet {
	clientID := pkt.ClientID()

	var preferredIP net.IP

	if device != nil && device.StaticIP != nil {
		preferredIP = device.StaticIP
	}

	if preferredIP == nil {
		if existing, ok := store.GetByClient(clientID); ok {
			switch existing.State {
			case lease.StateReserved, lease.StateOffered, lease.StateBound:
				preferredIP = existing.IP
			}
		}
	}

	if preferredIP == nil {
		if reqIP := pkt.Options.RequestedIP(); reqIP != nil {
			if cfg.Network.Contains(reqIP) {
				preferredIP = reqIP
			}
		}
	}

	var l lease.Lease
	var err error

	if preferredIP != nil {
		l, err = store.Allocate(clientID, preferredIP)
		if err != nil {
			preferredIP = nil
		}
	}

	if preferredIP == nil {
		ip, ok := store.NextAvailable()
		if !ok {
			slog.Warn("no IPs available", "subnet", cfg.Name, "client", clientID)
			return nil
		}
		l, err = store.Allocate(clientID, ip)
		if err != nil {
			slog.Error("allocate failed", "ip", ip, "client", clientID, "err", err)
			return nil
		}
	}

	reply := h.buildReply(pkt, cfg, l.IP, MsgOffer, cfg.LeaseTime)
	injectZTPOptions(reply, h.serverIP, pkt.CHAddr, profile)

	slog.Info("DHCPOFFER", "subnet", cfg.Name, "client", clientID, "ip", l.IP)
	return reply
}

func (h *Handler) handleRequest(
	pkt *Packet,
	cfg subnet.Config,
	store lease.Store,
	device *ztp.DeviceRecord,
	profile *ztp.VendorProfile,
) *Packet {
	clientID := pkt.ClientID()

	if svrID := pkt.Options.ServerID(); svrID != nil {
		if !svrID.Equal(h.serverIP) {
			return nil
		}
	}

	var requestedIP net.IP
	if pkt.CIAddr != nil && !pkt.CIAddr.IsUnspecified() {
		requestedIP = pkt.CIAddr
	} else if reqIP := pkt.Options.RequestedIP(); reqIP != nil {
		requestedIP = reqIP
	}

	if requestedIP == nil {
		slog.Warn("REQUEST has no requested IP", "client", clientID)
		return h.buildNak(pkt)
	}

	if device != nil && device.StaticIP != nil {
		if !device.StaticIP.Equal(requestedIP) {
			slog.Warn("REQUEST IP mismatch for static device",
				"client", clientID, "requested", requestedIP, "static", device.StaticIP)
			return h.buildNak(pkt)
		}
	}

	l, err := store.Renew(clientID, requestedIP)
	if err != nil {
		slog.Warn("REQUEST cannot be honoured", "client", clientID, "ip", requestedIP, "err", err)
		return h.buildNak(pkt)
	}

	reply := h.buildReply(pkt, cfg, l.IP, MsgAck, cfg.LeaseTime)
	injectZTPOptions(reply, h.serverIP, pkt.CHAddr, profile)

	h.plugins.EmitLeaseEvent(plugin.LeaseEvent{
		Type:       plugin.EventAssigned,
		IP:         l.IP,
		ClientID:   clientID,
		ClientHW:   pkt.CHAddr,
		GiAddr:     pkt.GIAddr,
		SubnetName: cfg.Name,
		SubnetCIDR: cfg.Network.String(),
		ExpiresAt:  l.ExpiresAt,
	})

	slog.Info("DHCPACK", "subnet", cfg.Name, "client", clientID, "ip", l.IP)
	return reply
}

func (h *Handler) handleRelease(pkt *Packet, cfg subnet.Config, store lease.Store) {
	clientID := pkt.ClientID()
	if pkt.CIAddr == nil || pkt.CIAddr.IsUnspecified() {
		return
	}

	if err := store.Release(clientID, pkt.CIAddr); err != nil {
		slog.Debug("DHCPRELEASE failed", "client", clientID, "ip", pkt.CIAddr, "err", err)
		return
	}

	h.plugins.EmitLeaseEvent(plugin.LeaseEvent{
		Type:       plugin.EventReleased,
		IP:         pkt.CIAddr,
		ClientID:   clientID,
		ClientHW:   pkt.CHAddr,
		GiAddr:     pkt.GIAddr,
		SubnetName: cfg.Name,
		SubnetCIDR: cfg.Network.String(),
	})

	slog.Info("DHCPRELEASE", "subnet", cfg.Name, "client", clientID, "ip", pkt.CIAddr)
}

func (h *Handler) handleDecline(pkt *Packet, cfg subnet.Config, store lease.Store) {
	ip := pkt.Options.RequestedIP()
	if ip == nil {
		ip = pkt.CIAddr
	}
	if ip == nil || ip.IsUnspecified() {
		return
	}

	if err := store.Decline(ip); err != nil {
		slog.Debug("DHCPDECLINE failed", "ip", ip, "err", err)
		return
	}

	h.plugins.EmitLeaseEvent(plugin.LeaseEvent{
		Type:       plugin.EventDeclined,
		IP:         ip,
		ClientID:   pkt.ClientID(),
		ClientHW:   pkt.CHAddr,
		GiAddr:     pkt.GIAddr,
		SubnetName: cfg.Name,
		SubnetCIDR: cfg.Network.String(),
	})

	slog.Warn("DHCPDECLINE (ARP conflict)", "subnet", cfg.Name, "ip", ip, "client", pkt.ClientID())
}

func (h *Handler) handleInform(pkt *Packet, cfg subnet.Config) *Packet {
	reply := h.buildReply(pkt, cfg, nil, MsgAck, 0)
	reply.YIAddr = net.IPv4zero.To4()
	return reply
}

func (h *Handler) buildReply(pkt *Packet, cfg subnet.Config, offerIP net.IP, msgType byte, leaseTime time.Duration) *Packet {
	reply := &Packet{
		Op:     OpReply,
		HType:  pkt.HType,
		HLen:   pkt.HLen,
		Hops:   0,
		XID:    pkt.XID,
		Secs:   0,
		Flags:  pkt.Flags,
		GIAddr: pkt.GIAddr,
		CHAddr: pkt.CHAddr,
	}

	if offerIP != nil {
		reply.YIAddr = offerIP.To4()
	}

	opts := make(Options)
	opts[OptMsgType] = []byte{msgType}
	opts[OptServerID] = EncodeIP(h.serverIP)

	if offerIP != nil && leaseTime > 0 {
		opts[OptLeaseTime] = EncodeUint32(uint32(leaseTime.Seconds()))
		opts[OptSubnetMask] = []byte(cfg.Network.Mask)
		if cfg.Router != nil {
			opts[OptRouter] = EncodeIP(cfg.Router)
		}
		if len(cfg.DNS) > 0 {
			opts[OptDNS] = EncodeIPs(cfg.DNS)
		}
		if cfg.BroadcastAddr != nil {
			opts[OptBroadcastAddress] = EncodeIP(cfg.BroadcastAddr)
		}
	}

	reply.Options = opts
	return reply
}

func (h *Handler) buildNak(pkt *Packet) *Packet {
	reply := &Packet{
		Op:     OpReply,
		HType:  pkt.HType,
		HLen:   pkt.HLen,
		XID:    pkt.XID,
		Flags:  pkt.Flags,
		GIAddr: pkt.GIAddr,
		CHAddr: pkt.CHAddr,
	}
	reply.Options = Options{
		OptMsgType:  {MsgNak},
		OptServerID: EncodeIP(h.serverIP),
	}
	return reply
}

func injectZTPOptions(pkt *Packet, serverIP net.IP, chaddr net.HardwareAddr, profile *ztp.VendorProfile) {
	if profile == nil {
		return
	}
	if pkt.Options == nil {
		pkt.Options = make(Options)
	}
	pkt.Options[OptTFTPServer] = []byte(serverIP.String())
	pkt.Options[OptBootfile] = []byte(MACNoDash(chaddr) + ".cfg")
}
