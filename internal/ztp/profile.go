package ztp

import (
	"bytes"
	"fmt"
	"net"
	"text/template"
)

// VendorProfile defines a ZTP configuration template for a class of devices.
type VendorProfile struct {
	Name           string
	MatchOption60  string
	ConfigTemplate string
}

// ProfileStore manages vendor profiles.
type ProfileStore interface {
	Add(VendorProfile) error
	Get(name string) (VendorProfile, bool)
	GetByOption60(option60 string) (VendorProfile, bool)
	Delete(name string) bool
	List() []VendorProfile
}

// TemplateData holds the variables injected into a config template.
type TemplateData struct {
	MAC        string
	MACNoDash  string
	IP         string
	SubnetMask string
	Router     string
	Hostname   string
	Vars       map[string]any
}

// RenderConfig renders the config template for a device.
func RenderConfig(profile VendorProfile, record DeviceRecord, ip net.IP, mask net.IPMask, router net.IP) ([]byte, error) {
	tmpl, err := template.New("config").Parse(profile.ConfigTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	mac := record.MAC
	var macStr string
	if len(mac) > 0 {
		b := make([]byte, 0, len(mac)*3)
		for i, octet := range mac {
			if i > 0 {
				b = append(b, ':')
			}
			const hextable = "0123456789abcdef"
			b = append(b, hextable[octet>>4], hextable[octet&0xf])
		}
		macStr = string(b)
	}

	var ipStr, maskStr string
	if ip != nil {
		ipStr = ip.String()
	}
	if mask != nil {
		maskStr = net.IP(mask).String()
	}

	var routerStr string
	if router != nil {
		routerStr = router.String()
	}

	data := TemplateData{
		MAC:        macStr,
		MACNoDash:  record.MACNoDash(),
		IP:         ipStr,
		SubnetMask: maskStr,
		Router:     routerStr,
		Hostname:   record.Hostname,
		Vars:       record.Vars,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}

