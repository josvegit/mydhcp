package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/josvegit/mydhcp/internal/plugin"
	"github.com/josvegit/mydhcp/internal/subnet"
)

//go:embed ui/dist
var uiFiles embed.FS

// Dashboard serves a React UI and streams DHCP lease events over SSE.
type Dashboard struct {
	plugin.BasePlugin

	listen  string
	subnets *subnet.Manager
	srv     *http.Server

	mu      sync.RWMutex
	clients map[chan []byte]struct{}
}

// Config holds Dashboard construction parameters.
type Config struct {
	Listen  string
	Subnets *subnet.Manager
}

func New(cfg Config) *Dashboard {
	return &Dashboard{
		listen:  cfg.Listen,
		subnets: cfg.Subnets,
		clients: make(map[chan []byte]struct{}),
	}
}

func (d *Dashboard) Name() string { return "dashboard" }

// Start launches the HTTP server. Call before registering with the plugin registry.
func (d *Dashboard) Start() error {
	static, err := fs.Sub(uiFiles, "ui/dist")
	if err != nil {
		return fmt.Errorf("dashboard: embed sub: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/api/state", d.handleState)
	mux.HandleFunc("/events", d.handleSSE)

	d.srv = &http.Server{
		Addr:    d.listen,
		Handler: mux,
	}

	go func() {
		slog.Info("dashboard listening", "addr", d.listen)
		if err := d.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("dashboard server error", "err", err)
		}
	}()
	return nil
}

// OnLeaseEvent broadcasts the event to all connected SSE clients.
func (d *Dashboard) OnLeaseEvent(ev plugin.LeaseEvent) error {
	evt := map[string]any{
		"type":        string(ev.Type),
		"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
		"ip":          ev.IP.String(),
		"client_id":   ev.ClientID,
		"subnet_name": ev.SubnetName,
		"subnet_cidr": ev.SubnetCIDR,
		"expires_at":  formatTime(ev.ExpiresAt),
	}
	if len(ev.GiAddr) > 0 && !ev.GiAddr.IsUnspecified() {
		evt["gi_addr"] = ev.GiAddr.String()
	}

	data, err := json.Marshal(map[string]any{"type": "lease_event", "event": evt})
	if err != nil {
		return fmt.Errorf("dashboard marshal: %w", err)
	}
	d.broadcast(data)
	return nil
}

// OnShutdown gracefully stops the HTTP server.
func (d *Dashboard) OnShutdown(ctx context.Context) error {
	if d.srv != nil {
		return d.srv.Shutdown(ctx)
	}
	return nil
}

func (d *Dashboard) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	type subnetInfo struct {
		Name      string `json:"name"`
		Network   string `json:"network"`
		Router    string `json:"router"`
		LeaseTime string `json:"lease_time"`
		Total     int    `json:"total"`
		Occupied  int    `json:"occupied"`
	}
	type leaseInfo struct {
		IP        string `json:"ip"`
		ClientID  string `json:"client_id"`
		State     string `json:"state"`
		Subnet    string `json:"subnet"`
		OfferedAt string `json:"offered_at,omitempty"`
		BoundAt   string `json:"bound_at,omitempty"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}

	subnets := d.subnets.List()
	snInfos := make([]subnetInfo, 0, len(subnets))
	var allLeases []leaseInfo

	for _, sn := range subnets {
		store, ok := d.subnets.GetLeaseStore(sn.Name)
		if !ok {
			continue
		}
		snInfos = append(snInfos, subnetInfo{
			Name:      sn.Name,
			Network:   sn.Network.String(),
			Router:    sn.Router.String(),
			LeaseTime: sn.LeaseTime.String(),
			Total:     countRange(sn.RangeStart, sn.RangeEnd),
			Occupied:  store.OccupiedCount(),
		})
		for _, l := range store.List() {
			li := leaseInfo{
				IP:       l.IP.String(),
				ClientID: l.ClientID,
				State:    l.State.String(),
				Subnet:   sn.Name,
			}
			if !l.OfferedAt.IsZero() {
				li.OfferedAt = l.OfferedAt.UTC().Format(time.RFC3339)
			}
			if !l.BoundAt.IsZero() {
				li.BoundAt = l.BoundAt.UTC().Format(time.RFC3339)
			}
			if !l.ExpiresAt.IsZero() {
				li.ExpiresAt = l.ExpiresAt.UTC().Format(time.RFC3339)
			}
			allLeases = append(allLeases, li)
		}
	}
	if allLeases == nil {
		allLeases = []leaseInfo{}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"subnets": snInfos,
		"leases":  allLeases,
	})
}

func (d *Dashboard) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := make(chan []byte, 32)
	d.subscribe(ch)
	defer d.unsubscribe(ch)

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (d *Dashboard) subscribe(ch chan []byte) {
	d.mu.Lock()
	d.clients[ch] = struct{}{}
	d.mu.Unlock()
}

func (d *Dashboard) unsubscribe(ch chan []byte) {
	d.mu.Lock()
	delete(d.clients, ch)
	d.mu.Unlock()
}

func (d *Dashboard) broadcast(data []byte) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for ch := range d.clients {
		select {
		case ch <- data:
		default: // slow client — drop event
		}
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func countRange(start, end net.IP) int {
	s, e := start.To4(), end.To4()
	if s == nil || e == nil {
		return 0
	}
	si := uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
	ei := uint32(e[0])<<24 | uint32(e[1])<<16 | uint32(e[2])<<8 | uint32(e[3])
	if ei < si {
		return 0
	}
	return int(ei-si) + 1
}
