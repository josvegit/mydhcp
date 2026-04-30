# mydhcp — Design

## Scope

mydhcp is a DHCPv4 server written in pure Go. It targets ISP and enterprise operators who need correct DHCP with built-in Zero-Touch Provisioning (ZTP) for network equipment: access switches, CPE routers, ONTs, and similar devices.

**ZTP in this context is a boot-time provisioning mechanism.** When a device powers on or factory-resets, it gets an IP and downloads its initial configuration via TFTP. Runtime device management (firmware upgrades, config pushes to running devices) is out of scope — that belongs to TR-069, NETCONF, or similar management-plane tools.

Design values, in priority order:

1. **Correctness** — RFC 2131 implemented faithfully for the common four-message exchange
2. **Simplicity** — zero required dependencies beyond the Go standard library
3. **Operability** — subnets, devices, and profiles managed at runtime via HTTP API without restarts; observable via structured logging and plugin hooks

### What it is not

- Not a DHCPv6 server
- Not a relay agent (`mydhcp relay` is a separate subcommand)
- Not a runtime device management system
- Not a production-grade HA server (no lease replication or failover)

### Binary subcommands

```
mydhcp server  [--config file]   DHCP server with ZTP (default)
mydhcp relay   [--config file]   Lightweight DHCP relay agent
mydhcp version                   Print version and exit
```

---

## DHCP Protocol

### Message flow (DORA)

```
Client                          Server
  |-- DHCPDISCOVER (broadcast) -->|
  |<-- DHCPOFFER   (uc/bc)   -----|
  |-- DHCPREQUEST  (broadcast) -->|
  |<-- DHCPACK     (uc/bc)   -----|
  |   or DHCPNAK                  |
```

When a relay agent is in the path the client sees the same exchange. The relay sets `giaddr` and unicasts to the server; the server replies unicast to the relay, which delivers to the client.

### Additional messages

| Message     | Direction       | Action |
|-------------|-----------------|--------|
| DHCPRELEASE | Client → Server | Return lease to pool (or to Reserved for static leases) |
| DHCPDECLINE | Client → Server | Mark IP Declined; hold for `decline_cooldown` |
| DHCPINFORM  | Client → Server | Reply with options, no IP assignment |
| DHCPNAK     | Server → Client | Sent when REQUEST cannot be honoured |

### DHCPDECLINE

The client ARP-probes the offered IP before accepting it. If another host responds, the client sends DHCPDECLINE. The server marks that IP **Declined** and withholds it from the pool for `decline_cooldown` (default 10 minutes). The reaper then returns it.

### Fixed fields processed in every packet

`op`, `htype`/`hlen`, `xid`, `ciaddr`, `chaddr`, `flags`, `giaddr`, `options` (magic cookie `99.130.83.99` + option list).

### Options

Parsed in requests:

| Option | Name | Purpose |
|--------|------|---------|
| 53 | Message Type | Required |
| 55 | Parameter Request List | Guides reply options |
| 50 | Requested IP | Honoured if available |
| 61 | Client Identifier | Overrides `chaddr` as identity when present |
| 60 | Vendor Class Identifier | ZTP vendor profile matching |

Included in replies:

| Option | Name | Notes |
|--------|------|-------|
| 53 | Message Type | |
| 54 | Server Identifier | `server_ip` from config |
| 51 | IP Address Lease Time | |
| 1  | Subnet Mask | |
| 3  | Router | |
| 6  | DNS Servers | |
| 28 | Broadcast Address | |
| 66 | TFTP Server Name | ZTP only — set to `server_ip` |
| 67 | Bootfile Name | ZTP only — set to `{macnodash}.cfg` |
| 255 | End | |

---

## Multi-Subnet Support via Relay Agents

### Single socket

The server binds one UDP socket to `0.0.0.0:67`. No per-interface binding, no VLAN subinterfaces on the server host. Relay agents on each VLAN's L3 gateway forward client broadcasts as unicast to the server, identifying the client subnet via the `giaddr` field.

### Subnet selection

```
packet arrives
  +-- giaddr != 0  →  find subnet whose CIDR contains giaddr        (relay case)
  +-- giaddr == 0  →  find subnet whose CIDR contains server_ip     (direct-connect)
  +-- no match     →  log warning, drop
```

### Reply routing

| `giaddr` | broadcast flag | Reply destination |
|----------|---------------|-------------------|
| != 0 | either | unicast to `giaddr:67` |
| == 0 | set | broadcast `255.255.255.255:68` |
| == 0 | clear | unicast to offered IP`:68` |

When `giaddr != 0` the reply goes to port **67** (the relay listens on 67 and re-broadcasts to the client on its VLAN).

---

## Package Layout

```
mydhcp/
├── cmd/
│   └── mydhcp/
│       └── main.go              # subcommand dispatch
│
├── internal/
│   ├── dhcp/
│   │   ├── server.go            # UDP socket, dispatch loop
│   │   ├── packet.go            # parse() / serialize()
│   │   ├── options.go           # option encode/decode
│   │   ├── handler.go           # per-message-type handlers
│   │   └── lease.go             # Lease struct, LeaseStore interface, LeaseState
│   │
│   ├── ztp/
│   │   ├── manager.go           # ZTPManager: coordinated device/profile/lease ops
│   │   ├── device.go            # DeviceRecord, DeviceStore interface
│   │   ├── profile.go           # VendorProfile, ProfileStore interface, template rendering
│   │   ├── memory.go            # in-memory DeviceStore + ProfileStore
│   │   └── tftp.go              # embedded TFTP server (RFC 1350, read-only)
│   │
│   ├── store/
│   │   └── memory.go            # in-memory LeaseStore implementation
│   │
│   ├── subnet/
│   │   └── manager.go           # RWMutex-protected subnet map
│   │
│   ├── plugin/
│   │   ├── plugin.go            # Plugin interface (event observer)
│   │   └── registry.go          # registration, event emission
│   │
│   ├── api/
│   │   ├── server.go            # HTTP server setup
│   │   └── handlers.go          # REST handlers
│   │
│   ├── relay/
│   │   └── relay.go             # mydhcp relay implementation
│   │
│   └── config/
│       └── config.go            # Config struct, YAML loader
│
├── plugins/
│   └── auditlog/
│       └── auditlog.go
│
├── go.mod
├── go.sum
└── README.md
```

### Dependency rules

- `internal/dhcp` imports `internal/ztp`, `internal/subnet`, `internal/store`, `internal/plugin`
- `internal/ztp` imports `internal/store`, `internal/subnet`
- `internal/api` imports `internal/subnet`, `internal/ztp`, `internal/store`
- `internal/plugin` imports nothing internal
- `plugins/auditlog` imports only `internal/plugin`
- Nothing imports from `plugins/`

---

## Core Server

### UDP listener

One socket bound to `0.0.0.0:67` with `SO_BROADCAST`. Requires elevated privileges or `CAP_NET_BIND_SERVICE`.

### Packet lifecycle

```
UDP datagram arrives
  │
  ├─ packet.Parse()
  ├─ validate magic cookie (drop if absent)
  ├─ subnet.Manager.ForPacket(pkt)          → SubnetConfig + LeaseStore; drop if no match
  ├─ ztp.Manager.LookupForPacket(chaddr, opt60)   → *DeviceRecord, *VendorProfile (nil if ZTP off)
  ├─ identify message type (Option 53)
  ├─ handler.Dispatch(subnet, leaseStore, device, profile)
  │     if device.StaticIP != nil: use as requestedIP in Allocate()
  │     if profile != nil: inject Option 66 + Option 67 into reply
  ├─ plugin.Registry.EmitLeaseEvent(...)    non-blocking; does not affect reply
  ├─ packet.Serialize()
  └─ send (per reply routing table above)
```

Plugin emission happens after the reply is fully assembled and sent. Plugins cannot affect the reply.

### Concurrency

| Goroutine | Accesses | Locking |
|-----------|----------|---------|
| DHCP dispatch | subnet map (read), lease store (rw), ZTP manager (read) | RLock each manager for lookup; release before handler |
| HTTP API | subnet map (rw), ZTP manager (rw) | Lock for writes, RLock for reads |
| Reaper ×N | one lease store per subnet (rw) | none — sole writer per store |
| TFTP server | ZTP manager (read) | RLock on ZTP manager |

`subnet.Manager` and `ztp.Manager` each hold their own `sync.RWMutex`, independent of each other. The DHCP goroutine holds at most one read lock at a time and releases it before any store operation.

---

## Plugins

Plugins are pure event observers. They cannot intercept packets, modify replies, or abort the handler. Their purpose is cross-cutting observability: audit logging, metrics export, external event streaming.

### Interface

```go
type Plugin interface {
    Name() string
    OnLeaseEvent(event LeaseEvent) error
    OnShutdown(ctx context.Context) error
}

type LeaseEvent struct {
    Type   LeaseEventType  // Assigned | Renewed | Released | Expired | Declined
    Lease  dhcp.Lease
    Subnet subnet.Config
}
```

`OnLeaseEvent` is called synchronously after the reply is sent. It must return quickly; plugins that need async I/O spawn their own goroutines internally.

`BasePlugin` provides no-op implementations for embedding. `OnShutdown` is called in reverse registration order.

---

## IP Lease Management

### LeaseStore interface

```go
type LeaseStore interface {
    Reserve    (clientID string, ip net.IP) error
    Unreserve  (ip net.IP) error
    Allocate   (clientID string, ip net.IP) (Lease, error)
    Renew      (clientID string, ip net.IP) (Lease, error)
    Release    (clientID string, ip net.IP) error
    Decline    (ip net.IP) error
    Get        (ip net.IP) (Lease, bool)
    GetByClient(clientID string) (Lease, bool)
    OccupiedCount() int   // Reserved + Offered + Bound
    Expired    () []Lease
    Declined   () []Lease
}
```

`clientID` is Option 61 when present, otherwise `chaddr` as a lowercase hex string.

`OccupiedCount` includes **Reserved** because a subnet with pre-provisioned devices that have not yet booted is not empty — deleting it would silently orphan those device records.

### Lease states

| State | Meaning |
|-------|---------|
| Reserved | IP pre-claimed for a specific client; no expiry; device record exists |
| Offered | OFFER sent; expires if no REQUEST within `offer_timeout` |
| Bound | ACK sent; active lease |
| Expired | Past `ExpiresAt`; reaper returns to pool |
| Declined | ARP conflict; held for `decline_cooldown` then returned |

### Lease struct

```go
type Lease struct {
    IP         net.IP
    ClientID   string
    ClientHW   net.HardwareAddr
    GiAddr     net.IP       // relay agent IP; zero for direct-connect
    State      LeaseState
    OfferedAt  time.Time
    BoundAt    time.Time    // zero if Offered or Reserved
    ExpiresAt  time.Time    // zero if Reserved
    DeclinedAt time.Time    // zero unless Declined
}
```

`GiAddr` is stored for operational visibility (which relay agent / VLAN did this device come from).

### Lifecycle

```
  [Reserved] ←─ ZTPManager.AddDevice() with StaticIP
      │
      │ Allocate(rightClient, staticIP)
      ↓
  [Offered]  ←─ DISCOVER received, OFFER sent
      │            expires → back to Reserved (static) or free pool (dynamic)
      │ Renew()
      ↓
  [Bound]    ←─ REQUEST received, ACK sent
      │
      ├─ Renew()    → stays Bound, new ExpiresAt
      ├─ Release()  → [Reserved]  if a reservation exists for this IP (static device)
      │             → [Available] otherwise (dynamic)
      ├─ Decline()  → [Declined]
      └─ timeout    → [Expired] → reaper → [Reserved] (static) or [Available] (dynamic)

  [Declined]  cooldown elapsed → reaper → [Available]
              (static declined IPs also return to [Reserved] after cooldown)
```

`Release()` and the reaper both check whether a reservation exists for the IP before deciding where to return it. This single check handles the static vs. dynamic distinction uniformly.

### Reaper

One goroutine per subnet, waking every `lease_reaper_interval` (default 60s):

1. `store.Expired()` — transitions to Reserved or Available; emits `EventExpired` to plugins.
2. `store.Declined()` — transitions to Reserved or Available; no plugin event.

Reapers start when a subnet is added (including at server startup) and stop when a subnet is removed via the API.

### In-memory store

`ip → Lease` map and `clientID → Lease` map. Available IPs in a FIFO slice. Reserved IPs are in both maps but excluded from the FIFO.

---

## Zero-Touch Provisioning (ZTP)

ZTP is a first-class feature of `mydhcp server`, not a plugin. It is optional: set `ztp.enabled = false` to disable it entirely; `ztp.Manager` is nil and all ZTP paths in the handler are skipped.

**Scope:** ZTP handles initial device provisioning at boot time. A device boots, gets an IP, gets Option 66/67 pointing at the TFTP server, and downloads its configuration. That is the full scope. Ongoing runtime management of running devices is handled by separate tooling (TR-069, NETCONF, SSH, etc.).

### Storage interfaces

```go
// internal/ztp/device.go
type DeviceStore interface {
    Add    (DeviceRecord) error
    Get    (mac net.HardwareAddr) (DeviceRecord, bool)
    Delete (mac net.HardwareAddr) bool
    List   () []DeviceRecord
}

// internal/ztp/profile.go
type ProfileStore interface {
    Add          (VendorProfile) error
    Get          (name string) (VendorProfile, bool)
    GetByOption60(option60 string) (VendorProfile, bool)
    Delete       (name string) bool
    List         () []VendorProfile
}
```

Both have in-memory implementations in `internal/ztp/memory.go`. Persistent backends implement the same interfaces and are swapped in at construction — identical to how `LeaseStore` works.

### ZTPManager

Single entry point for all ZTP operations. Owns `DeviceStore`, `ProfileStore`, and a reference to `subnet.Manager` for coordinating lease reservations.

```go
type Manager struct {
    mu       sync.RWMutex
    devices  DeviceStore
    profiles ProfileStore
    subnets  *subnet.Manager
}
```

**Read methods** (used by DHCP handler and TFTP server, take RLock):
- `LookupForPacket(chaddr, option60) (*DeviceRecord, *VendorProfile)`
- `LookupForTFTP(mac) (*DeviceRecord, *VendorProfile, error)`

**Write methods** (used by HTTP API, take write lock):
- `AddDevice(record DeviceRecord) error`
- `DeleteDevice(mac net.HardwareAddr) error`
- `AddProfile(profile VendorProfile) error`
- `DeleteProfile(name string) error`

### Atomic device operations

The invariant: *a `Reserved` lease entry exists in a `LeaseStore` if and only if a `DeviceRecord` with that `StaticIP` exists in the `DeviceStore`.*

**AddDevice (with StaticIP):**
```
1. Lock ZTPManager
2. Find subnet whose CIDR contains StaticIP — ErrNoSubnet if not found
3. Validate StaticIP is within subnet's range — ErrOutOfRange if not
4. leaseStore.Reserve(device.ClientID(), StaticIP)
      fails (already reserved for another client) → return error, nothing written
5. devices.Add(record)
      fails → leaseStore.Unreserve(StaticIP) as compensation, return error
6. Unlock — success
```

**DeleteDevice (with StaticIP):**
```
1. Lock ZTPManager
2. Look up record — ErrNotFound if missing
3. leaseStore.Unreserve(StaticIP) — fails → return error, nothing deleted
4. devices.Delete(mac)            — in-memory: cannot fail
5. Unlock — success
```

For future persistent backends: wrap steps 3-4 in a database transaction.

### DeviceRecord

```go
type DeviceRecord struct {
    MAC           net.HardwareAddr   // primary key
    VendorProfile string             // VendorProfile.Name; empty = no config download
    StaticIP      net.IP             // nil = allocate from pool
    Hostname      string
    Vars          map[string]any     // extra template variables
}

func (d DeviceRecord) ClientID() string  // MAC as lowercase hex string
```

### VendorProfile

```go
type VendorProfile struct {
    Name           string   // unique identifier
    MatchOption60  string   // exact match against Option 60; empty = manual assignment only
    ConfigTemplate string   // Go text/template source
}
```

Filename served over TFTP is always `{macnodash}.cfg` (e.g. `aabbccddeeff.cfg`). No configurable filename pattern — keeping it simple.

### Template variables

| Variable | Value |
|----------|-------|
| `.MAC` | `aa:bb:cc:dd:ee:ff` |
| `.MACNoDash` | `aabbccddeeff` |
| `.IP` | assigned IP address (static devices only — see below) |
| `.SubnetMask` | e.g. `255.255.255.0` |
| `.Router` | default gateway |
| `.Hostname` | from device record |
| `.Vars` | `map[string]any` from device record |

Template engine: Go `text/template`.

### IP availability in templates

`.IP` is populated from `DeviceRecord.StaticIP`. Dynamic devices (no StaticIP) do not get a config file in v1 — the TFTP server returns error code 2 with the message `"config not available for dynamically-assigned devices"`. Config rendering for dynamic IPs is deferred (see Open Questions).

### Embedded TFTP server

RFC 1350, read requests only (WRQ rejected with error 2). One goroutine, bound to `0.0.0.0:69` (configurable). No filesystem — configs rendered in memory on demand, so TFTP always serves the latest content.

```
RRQ arrives for "aabbccddeeff.cfg"
  │
  ├─ parse MAC from filename          error 0 if unparseable
  ├─ ZTPManager.LookupForTFTP(mac)
  │     device not found              error 1 "File Not Found"
  │     no StaticIP                   error 2 "not available for dynamic devices"
  │     profile not found             error 1 "File Not Found"
  ├─ render ConfigTemplate            error 0 + detail if template fails
  └─ stream as DATA packets (512-byte blocks)
```

Port 69 requires elevated privileges. For environments without `CAP_NET_BIND_SERVICE`, set `ztp.tftp.listen` to a non-privileged port and point devices accordingly.

---

## Runtime HTTP API

Served on `127.0.0.1:8067` by default. All bodies are JSON. No authentication in v1 — binding to `127.0.0.1` provides implicit access control.

### Subnet endpoints

```
GET    /subnets              list subnets with per-subnet OccupiedCount
POST   /subnets              add subnet; starts reaper immediately
DELETE /subnets/{name}       remove subnet
GET    /leases               list leases; ?subnet={name} to filter
```

`DELETE /subnets/{name}` rejects with `409` if `OccupiedCount() > 0`. The response body breaks down the count by state to help the operator understand what's blocking:

```json
{ "error": "subnet has 13 occupied leases", "reserved": 8, "offered": 1, "bound": 4 }
```

### ZTP endpoints

```
GET    /devices                list device records
POST   /devices                add or update a device record
GET    /devices/{mac}          get device record
DELETE /devices/{mac}          remove device record

GET    /vendor-profiles        list vendor profiles
POST   /vendor-profiles        add or update a vendor profile
GET    /vendor-profiles/{name}
DELETE /vendor-profiles/{name}

GET    /configs/{mac}          render and return config over HTTP (debug/preview)
```

All ZTP writes go through `ZTPManager` methods — never directly to `DeviceStore` or `ProfileStore`. This ensures the reservation invariant is always maintained.

`POST /devices` errors:
- `static_ip` not within any configured subnet → `422`
- `static_ip` already reserved for a different client → `409`
- `vendor_profile` names a non-existent profile → `422`

`DELETE /vendor-profiles/{name}` is allowed even if device records reference it. The response includes a warning listing affected MACs. Affected devices get a TFTP error until reassigned.

---

## Configuration

### mydhcp server

```yaml
server:
  listen:    "0.0.0.0:67"
  server_ip: "192.0.2.1"     # Option 54 (server identifier) and Option 66 (TFTP server)

api:
  listen: "127.0.0.1:8067"

ztp:
  enabled: true
  tftp:
    listen: "0.0.0.0:69"

subnets:
  - name: "vlan100"
    network: "10.1.100.0/24"
    router: "10.1.100.1"
    dns: ["8.8.8.8"]
    lease_time: "24h"
    offer_timeout: "30s"
    decline_cooldown: "10m"
    lease_reaper_interval: "60s"
    range_start: "10.1.100.100"
    range_end:   "10.1.100.200"

logging:
  level: "info"      # debug | info | warn | error
  format: "text"     # text | json

plugins:
  - name: auditlog
    options:
      path: /var/log/mydhcp/audit.log
```

Subnets defined in the config file are loaded at startup. Additional subnets can be added at runtime via the API. Device records and vendor profiles are managed exclusively via the API — they are not persisted to disk in v1 and are lost on restart.

### mydhcp relay

```yaml
relay:
  listen:   "0.0.0.0:67"
  upstream: "192.0.2.1:67"
  interfaces:
    - name:     "eth0.100"
      agent_ip: "10.1.100.1"
    - name:     "eth0.200"
      agent_ip: "10.1.200.1"
```

The relay agent binds to per-VLAN interfaces — this is appropriate because the relay is physically on each VLAN and must set `giaddr` to its own IP on that segment.

### Config loading order

1. Built-in defaults
2. YAML file (`--config`, default `/etc/mydhcp/config.yaml`) — missing file is fatal
3. CLI flags override individual fields

---

## mydhcp relay

`internal/relay/relay.go` — separate code path, no shared logic with the server.

**What it does:**
1. Binds to UDP 67 on each configured interface.
2. Client broadcast arrives → sets `giaddr` to interface's `agent_ip` → unicasts to `upstream`.
3. Server reply arrives addressed to `giaddr` → forwards to client per `flags` field.

**What it does not do:** no lease tracking, no ZTP, no plugins, no HTTP API.

---

## Open Questions / Deferred Work

### Requires a decision before v1 ships

1. **Broadcast reply source IP** — when `giaddr == 0` and broadcast flag is set, the reply goes to `255.255.255.255:68` and must carry the correct source IP. On Linux, `IP_PKTINFO` on the receive socket provides the destination address of the incoming request, which is used as the source of the reply. Platform support (Linux vs macOS) to be verified during implementation.

2. **Client identity tie-breaking** — if a client sends Option 61 on DISCOVER but not on REQUEST (or vice versa), the `clientID` derived from each message differs. Before concluding a device has no existing lease, fall back to `chaddr`-derived identity if the Option-61-based `GetByClient` finds nothing.

3. **Overlapping subnet validation** — `POST /subnets` must reject CIDRs that overlap an existing subnet. Rule: strict no-overlap (no subnet may be a subset of another). Enforced in `subnet.Manager.Add()`.

4. **Declined static IPs** — if a statically-assigned device declines its IP (ARP conflict), the IP transitions to `Declined`. After `decline_cooldown` the reaper must return it to `Reserved` (not the free pool). The reaper uses the same reservation-check as `Release()` to determine this.

### Deferred to post-v1

| Area | Note |
|---|---|
| Lease persistence | `LeaseStore` interface is the swap point; SQLite candidate |
| ZTP data persistence | `DeviceStore` + `ProfileStore` interfaces are the swap points |
| Dynamic-IP config rendering | TFTP handler would need to query lease stores for the assigned IP |
| HTTP config delivery | Some devices (Cisco PnP, Juniper) prefer HTTP over TFTP |
| Option 43 vendor-specific | Some vendors use sub-option encoding instead of Option 66/67 |
| Option 82 relay info | Useful for surfacing circuit-ID / VLAN-ID as template variables |
| API authentication | Bearer token or mTLS; 127.0.0.1 default is sufficient for v1 |
| Startup seeding from file | Device records and profiles seeded from a flat file on startup |
| Prometheus metrics | Implement as a plugin using `OnLeaseEvent` |
| Config hot-reload | Requires careful handling of subnet range changes against live leases |
