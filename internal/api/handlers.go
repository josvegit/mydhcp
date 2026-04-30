package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/josvegit/mydhcp/internal/store"
	"github.com/josvegit/mydhcp/internal/subnet"
	"github.com/josvegit/mydhcp/internal/ztp"
)

type handlers struct {
	subnets *subnet.Manager
	ztpMgr  *ztp.Manager
}

// --- Subnet endpoints ---

type subnetRequest struct {
	Name                string   `json:"name"`
	Network             string   `json:"network"`
	Router              string   `json:"router"`
	DNS                 []string `json:"dns"`
	LeaseTime           string   `json:"lease_time"`
	OfferTimeout        string   `json:"offer_timeout"`
	DeclineCooldown     string   `json:"decline_cooldown"`
	LeaseReaperInterval string   `json:"lease_reaper_interval"`
	RangeStart          string   `json:"range_start"`
	RangeEnd            string   `json:"range_end"`
}

type subnetResponse struct {
	Name          string `json:"name"`
	Network       string `json:"network"`
	Router        string `json:"router"`
	OccupiedCount int    `json:"occupied_count"`
}

func (h *handlers) listSubnets(w http.ResponseWriter, r *http.Request) {
	cfgs := h.subnets.List()
	resp := make([]subnetResponse, 0, len(cfgs))
	for _, cfg := range cfgs {
		occupied := 0
		if ms, ok := h.subnets.GetStore(cfg.Name); ok {
			occupied = ms.OccupiedCount()
		}
		resp = append(resp, subnetResponse{
			Name:          cfg.Name,
			Network:       cfg.Network.String(),
			Router:        cfg.Router.String(),
			OccupiedCount: occupied,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handlers) addSubnet(w http.ResponseWriter, r *http.Request) {
	var req subnetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %s", err))
		return
	}

	cfg, err := parseSubnetConfig(req)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if err := h.subnets.Add(cfg); err != nil {
		if errors.Is(err, subnet.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, err.Error())
		} else if errors.Is(err, subnet.ErrOverlap) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}

	slog.Info("subnet added via API", "name", cfg.Name, "network", cfg.Network)
	writeJSON(w, http.StatusCreated, map[string]string{"name": cfg.Name})
}

func (h *handlers) deleteSubnet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	if err := h.subnets.Remove(name); err != nil {
		if errors.Is(err, subnet.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if errors.Is(err, subnet.ErrHasOccupied) {
			ms, ok := h.subnets.GetStore(name)
			if ok {
				reserved, offered, bound := ms.OccupiedByState()
				total := reserved + offered + bound
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":    fmt.Sprintf("subnet has %d occupied leases", total),
					"reserved": reserved,
					"offered":  offered,
					"bound":    bound,
				})
			} else {
				writeError(w, http.StatusConflict, err.Error())
			}
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) listLeases(w http.ResponseWriter, r *http.Request) {
	subnetFilter := r.URL.Query().Get("subnet")

	type leaseResponse struct {
		IP         string `json:"ip"`
		ClientID   string `json:"client_id"`
		ClientHW   string `json:"client_hw,omitempty"`
		State      string `json:"state"`
		SubnetName string `json:"subnet"`
		ExpiresAt  string `json:"expires_at,omitempty"`
	}

	var results []leaseResponse

	for _, cfg := range h.subnets.List() {
		if subnetFilter != "" && cfg.Name != subnetFilter {
			continue
		}
		ls, ok := h.subnets.GetLeaseStore(cfg.Name)
		if !ok {
			continue
		}
		for _, l := range ls.List() {
			lr := leaseResponse{
				IP:         l.IP.String(),
				ClientID:   l.ClientID,
				State:      l.State.String(),
				SubnetName: cfg.Name,
			}
			if len(l.ClientHW) > 0 {
				lr.ClientHW = l.ClientHW.String()
			}
			if !l.ExpiresAt.IsZero() {
				lr.ExpiresAt = l.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
			}
			results = append(results, lr)
		}
	}

	if results == nil {
		results = []leaseResponse{}
	}
	writeJSON(w, http.StatusOK, results)
}

// --- Device endpoints ---

type deviceRequest struct {
	MAC           string         `json:"mac"`
	VendorProfile string         `json:"vendor_profile"`
	StaticIP      string         `json:"static_ip,omitempty"`
	Hostname      string         `json:"hostname,omitempty"`
	Vars          map[string]any `json:"vars,omitempty"`
}

type deviceResponse struct {
	MAC           string         `json:"mac"`
	VendorProfile string         `json:"vendor_profile,omitempty"`
	StaticIP      string         `json:"static_ip,omitempty"`
	Hostname      string         `json:"hostname,omitempty"`
	Vars          map[string]any `json:"vars,omitempty"`
}

func deviceToResponse(d ztp.DeviceRecord) deviceResponse {
	r := deviceResponse{
		MAC:           d.MAC.String(),
		VendorProfile: d.VendorProfile,
		Hostname:      d.Hostname,
		Vars:          d.Vars,
	}
	if d.StaticIP != nil {
		r.StaticIP = d.StaticIP.String()
	}
	return r
}

func (h *handlers) listDevices(w http.ResponseWriter, r *http.Request) {
	devices := h.ztpMgr.ListDevices()
	resp := make([]deviceResponse, 0, len(devices))
	for _, d := range devices {
		resp = append(resp, deviceToResponse(d))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handlers) addDevice(w http.ResponseWriter, r *http.Request) {
	var req deviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %s", err))
		return
	}

	mac, err := net.ParseMAC(req.MAC)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("invalid MAC: %s", err))
		return
	}

	record := ztp.DeviceRecord{
		MAC:           mac,
		VendorProfile: req.VendorProfile,
		Hostname:      req.Hostname,
		Vars:          req.Vars,
	}

	if req.StaticIP != "" {
		ip := net.ParseIP(req.StaticIP)
		if ip == nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid static_ip")
			return
		}
		record.StaticIP = ip.To4()
	}

	if err := h.ztpMgr.AddDevice(record); err != nil {
		switch {
		case errors.Is(err, ztp.ErrNoSubnet):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, store.ErrAlreadyReserved):
			writeError(w, http.StatusConflict, "static_ip already reserved for a different client")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusCreated, deviceToResponse(record))
}

func (h *handlers) getDevice(w http.ResponseWriter, r *http.Request) {
	mac, err := net.ParseMAC(r.PathValue("mac"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid MAC address")
		return
	}

	d, ok := h.ztpMgr.GetDevice(mac)
	if !ok {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, deviceToResponse(d))
}

func (h *handlers) deleteDevice(w http.ResponseWriter, r *http.Request) {
	mac, err := net.ParseMAC(r.PathValue("mac"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid MAC address")
		return
	}

	if err := h.ztpMgr.DeleteDevice(mac); err != nil {
		if errors.Is(err, ztp.ErrDeviceNotFound) {
			writeError(w, http.StatusNotFound, "device not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Vendor profile endpoints ---

type profileRequest struct {
	Name           string `json:"name"`
	MatchOption60  string `json:"match_option60,omitempty"`
	ConfigTemplate string `json:"config_template"`
}

type profileResponse struct {
	Name           string `json:"name"`
	MatchOption60  string `json:"match_option60,omitempty"`
	ConfigTemplate string `json:"config_template"`
}

func profileToResponse(p ztp.VendorProfile) profileResponse {
	return profileResponse{
		Name:           p.Name,
		MatchOption60:  p.MatchOption60,
		ConfigTemplate: p.ConfigTemplate,
	}
}

func (h *handlers) listProfiles(w http.ResponseWriter, r *http.Request) {
	profiles := h.ztpMgr.ListProfiles()
	resp := make([]profileResponse, 0, len(profiles))
	for _, p := range profiles {
		resp = append(resp, profileToResponse(p))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handlers) addProfile(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %s", err))
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}

	profile := ztp.VendorProfile{
		Name:           req.Name,
		MatchOption60:  req.MatchOption60,
		ConfigTemplate: req.ConfigTemplate,
	}

	if err := h.ztpMgr.AddProfile(profile); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, profileToResponse(profile))
}

func (h *handlers) getProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, ok := h.ztpMgr.GetProfile(name)
	if !ok {
		writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	writeJSON(w, http.StatusOK, profileToResponse(p))
}

func (h *handlers) deleteProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	affected, err := h.ztpMgr.DeleteProfile(name)
	if err != nil {
		if errors.Is(err, ztp.ErrProfileNotFound) {
			writeError(w, http.StatusNotFound, "profile not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	resp := map[string]any{"deleted": name}
	if len(affected) > 0 {
		resp["warning"] = fmt.Sprintf("profile still referenced by %d device(s)", len(affected))
		resp["affected_macs"] = affected
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- Config preview endpoint ---

func (h *handlers) getConfig(w http.ResponseWriter, r *http.Request) {
	macStr := r.PathValue("mac")
	mac, err := net.ParseMAC(macStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid MAC address")
		return
	}

	device, profile, err := h.ztpMgr.LookupForTFTP(mac)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	var mask net.IPMask
	var router net.IP
	if device.StaticIP != nil {
		if cfg, _, ok := h.subnets.SubnetForIP(device.StaticIP); ok {
			mask = cfg.Network.Mask
			router = cfg.Router
		}
	}

	content, err := ztp.RenderConfig(*profile, *device, device.StaticIP, mask, router)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("template error: %s", err))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(content) //nolint:errcheck
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("JSON encode error", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func parseSubnetConfig(req subnetRequest) (subnet.Config, error) {
	if req.Name == "" {
		return subnet.Config{}, fmt.Errorf("name is required")
	}
	_, network, err := net.ParseCIDR(req.Network)
	if err != nil {
		return subnet.Config{}, fmt.Errorf("invalid network: %s", err)
	}

	router := net.ParseIP(req.Router)
	if router == nil {
		return subnet.Config{}, fmt.Errorf("invalid router: %s", req.Router)
	}

	rangeStart := net.ParseIP(req.RangeStart)
	rangeEnd := net.ParseIP(req.RangeEnd)
	if rangeStart == nil || rangeEnd == nil {
		return subnet.Config{}, fmt.Errorf("invalid range_start or range_end")
	}

	var dnsIPs []net.IP
	for _, d := range req.DNS {
		ip := net.ParseIP(strings.TrimSpace(d))
		if ip == nil {
			return subnet.Config{}, fmt.Errorf("invalid DNS IP: %s", d)
		}
		dnsIPs = append(dnsIPs, ip)
	}

	leaseTime, err := parseDuration(req.LeaseTime, "24h")
	if err != nil {
		return subnet.Config{}, fmt.Errorf("invalid lease_time: %s", err)
	}
	offerTimeout, err := parseDuration(req.OfferTimeout, "30s")
	if err != nil {
		return subnet.Config{}, fmt.Errorf("invalid offer_timeout: %s", err)
	}
	declineCooldown, err := parseDuration(req.DeclineCooldown, "10m")
	if err != nil {
		return subnet.Config{}, fmt.Errorf("invalid decline_cooldown: %s", err)
	}
	reaperInterval, err := parseDuration(req.LeaseReaperInterval, "60s")
	if err != nil {
		return subnet.Config{}, fmt.Errorf("invalid lease_reaper_interval: %s", err)
	}

	return subnet.Config{
		Name:                req.Name,
		Network:             network,
		Router:              router.To4(),
		DNS:                 dnsIPs,
		BroadcastAddr:       subnet.BroadcastAddr(network),
		LeaseTime:           leaseTime,
		OfferTimeout:        offerTimeout,
		DeclineCooldown:     declineCooldown,
		LeaseReaperInterval: reaperInterval,
		RangeStart:          rangeStart.To4(),
		RangeEnd:            rangeEnd.To4(),
	}, nil
}

func parseDuration(s, def string) (time.Duration, error) {
	if s == "" {
		s = def
	}
	return time.ParseDuration(s)
}
