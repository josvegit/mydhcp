import { useState, useEffect, useCallback } from 'react'

// ── constants ────────────────────────────────────────────────────────────────

const EVENT_COLORS = {
  offered:  '#a78bfa',
  assigned: '#22c55e',
  renewed:  '#3b82f6',
  released: '#94a3b8',
  expired:  '#f97316',
  declined: '#ef4444',
}

const STATE_COLORS = {
  bound:    '#22c55e',
  offered:  '#a78bfa',
  reserved: '#a78bfa',
  expired:  '#f97316',
  declined: '#ef4444',
}

// ── helpers ──────────────────────────────────────────────────────────────────

function fmtTime(iso) {
  if (!iso) return '—'
  return new Date(iso).toLocaleTimeString('en-US', {
    hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit',
  })
}

function fmtDateTime(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  return d.toLocaleDateString('en-US', { month: 'short', day: '2-digit' }) + ' ' +
    d.toLocaleTimeString('en-US', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function shortID(id) {
  if (!id) return '—'
  // If it looks like a MAC (xx:xx:xx:xx:xx:xx), show last 3 octets
  const p = id.split(':')
  if (p.length === 6) return p.slice(-3).join(':')
  // Otherwise truncate
  return id.length > 12 ? id.slice(-12) : id
}

function timeUntil(iso) {
  if (!iso) return '—'
  const diff = Math.floor((new Date(iso).getTime() - Date.now()) / 1000)
  if (diff <= 0) return 'expired'
  if (diff < 60) return `${diff}s`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ${diff % 60}s`
  return `${Math.floor(diff / 3600)}h ${Math.floor((diff % 3600) / 60)}m`
}

// ── sub-components ────────────────────────────────────────────────────────────

function Badge({ label, color }) {
  return (
    <span style={{
      display: 'inline-block', padding: '2px 7px', borderRadius: 4,
      fontSize: 11, fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em',
      background: color + '22', color, border: `1px solid ${color}44`,
      minWidth: 70, textAlign: 'center',
    }}>
      {label}
    </span>
  )
}

function StatCard({ label, value, color }) {
  return (
    <div className="stat-card">
      <div className="stat-value" style={{ color }}>{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  )
}

function EventFeed({ events }) {
  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">Live Events</span>
        <span className="card-count">{events.length} shown</span>
      </div>
      <div className="event-list">
        {events.length === 0 ? (
          <div className="empty">Waiting for DHCP events…</div>
        ) : events.map((ev, i) => (
          <div key={ev._id} className={`event-item${i === 0 ? ' event-new' : ''}`}>
            <span className="ev-time">{fmtTime(ev.timestamp)}</span>
            <Badge label={ev.type} color={EVENT_COLORS[ev.type] || '#94a3b8'} />
            <span className="ev-ip">{ev.ip}</span>
            <span className="ev-mac">{shortID(ev.client_id)}</span>
            <span className="ev-subnet">{ev.subnet_name}</span>
          </div>
        ))}
      </div>
    </div>
  )
}

function SubnetGrid({ subnets, leases }) {
  const leaseArr = Object.values(leases)
  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">Subnets</span>
      </div>
      {subnets.length === 0 ? (
        <div className="empty">No subnets configured</div>
      ) : subnets.map(sn => {
        const occupied = leaseArr.filter(l => l.subnet === sn.name).length
        const pct = sn.total > 0 ? Math.round((occupied / sn.total) * 100) : 0
        const barColor = pct > 80 ? '#ef4444' : pct > 50 ? '#f97316' : '#22c55e'
        return (
          <div key={sn.name} className="subnet-card">
            <div className="subnet-header">
              <span className="subnet-name">{sn.name}</span>
              <span className="subnet-net">{sn.network}</span>
            </div>
            <div className="bar-bg">
              <div className="bar-fill" style={{ width: `${pct}%`, background: barColor }} />
            </div>
            <div className="subnet-footer">
              <span>{occupied}/{sn.total} occupied · {pct}%</span>
              <span>GW {sn.router} · {sn.lease_time}</span>
            </div>
          </div>
        )
      })}
    </div>
  )
}

function LeaseDetail({ lease }) {
  return (
    <tr className="detail-row">
      <td colSpan={5}>
        <div className="detail-grid">
          <div className="detail-item">
            <span className="detail-label">Full Client ID</span>
            <span className="detail-value mono">{lease.client_id || '—'}</span>
          </div>
          <div className="detail-item">
            <span className="detail-label">Offered At</span>
            <span className="detail-value mono">{fmtDateTime(lease.offered_at)}</span>
          </div>
          <div className="detail-item">
            <span className="detail-label">Bound At</span>
            <span className="detail-value mono">{fmtDateTime(lease.bound_at)}</span>
          </div>
          <div className="detail-item">
            <span className="detail-label">Expires At</span>
            <span className="detail-value mono">{fmtDateTime(lease.expires_at)}</span>
          </div>
          <div className="detail-item">
            <span className="detail-label">Subnet</span>
            <span className="detail-value mono">{lease.subnet}</span>
          </div>
        </div>
      </td>
    </tr>
  )
}

function LeaseTable({ leases, tick }) {
  const [expanded, setExpanded] = useState(null)
  const arr = Object.values(leases).sort((a, b) =>
    a.ip.localeCompare(b.ip, undefined, { numeric: true })
  )
  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">Active Leases</span>
        <span className="card-count">{arr.length} leases</span>
      </div>
      {arr.length === 0 ? (
        <div className="empty">No active leases</div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>IP Address</th>
                <th>Client ID</th>
                <th>State</th>
                <th>Subnet</th>
                <th>Expires In</th>
              </tr>
            </thead>
            <tbody>
              {arr.map(l => (
                <>
                  <tr
                    key={l.ip}
                    className={`lease-row${expanded === l.ip ? ' row-expanded' : ''}`}
                    onClick={() => setExpanded(expanded === l.ip ? null : l.ip)}
                    style={{ cursor: 'pointer' }}
                  >
                    <td className="mono bold">{l.ip}</td>
                    <td className="mono">{l.client_id}</td>
                    <td><Badge label={l.state} color={STATE_COLORS[l.state] || '#94a3b8'} /></td>
                    <td>{l.subnet}</td>
                    <td className="muted">{timeUntil(l.expires_at)}</td>
                  </tr>
                  {expanded === l.ip && <LeaseDetail key={`${l.ip}-detail`} lease={l} />}
                </>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// ── App ───────────────────────────────────────────────────────────────────────

export default function App() {
  const [subnets,     setSubnets]     = useState([])
  const [leases,      setLeases]      = useState({})
  const [events,      setEvents]      = useState([])
  const [connected,   setConnected]   = useState(false)
  const [totalEvents, setTotalEvents] = useState(0)
  const [tick,        setTick]        = useState(0)

  // Refresh expiry countdowns every 30 s
  useEffect(() => {
    const id = setInterval(() => setTick(t => t + 1), 30_000)
    return () => clearInterval(id)
  }, [])

  const applyEvent = useCallback((ev) => {
    setTotalEvents(n => n + 1)
    setEvents(prev => [{ ...ev, _id: `${Date.now()}-${ev.ip}` }, ...prev].slice(0, 100))
    setLeases(prev => {
      const next = { ...prev }
      if (ev.type === 'released' || ev.type === 'expired') {
        delete next[ev.ip]
      } else if (ev.type === 'declined') {
        next[ev.ip] = { ...(prev[ev.ip] || {}), ip: ev.ip, state: 'declined',
          client_id: ev.client_id, subnet: ev.subnet_name, expires_at: '' }
      } else if (ev.type === 'offered') {
        // Only set offered state if not already bound (avoid overwriting a fast ACK)
        if (!prev[ev.ip] || prev[ev.ip].state !== 'bound') {
          next[ev.ip] = { ...(prev[ev.ip] || {}), ip: ev.ip, state: 'offered',
            client_id: ev.client_id, subnet: ev.subnet_name, expires_at: ev.expires_at }
        }
      } else {
        // assigned or renewed → bound
        next[ev.ip] = {
          ...(prev[ev.ip] || {}),
          ip:         ev.ip,
          client_id:  ev.client_id,
          state:      'bound',
          subnet:     ev.subnet_name,
          expires_at: ev.expires_at,
        }
      }
      return next
    })
  }, [])

  const syncState = useCallback(() => {
    fetch('/api/state')
      .then(r => r.json())
      .then(data => {
        setSubnets(data.subnets || [])
        const m = {}
        for (const l of (data.leases || [])) m[l.ip] = l
        setLeases(m)
      })
      .catch(() => {})
  }, [])

  // Re-sync every 30 s to catch declined-cooldown releases (no event fires for those)
  useEffect(() => {
    const id = setInterval(syncState, 30_000)
    return () => clearInterval(id)
  }, [syncState])

  useEffect(() => {
    syncState()

    const es = new EventSource('/events')
    es.onopen  = () => setConnected(true)
    es.onerror = () => setConnected(false)
    es.onmessage = (e) => {
      try {
        const msg = JSON.parse(e.data)
        if (msg.type === 'lease_event') applyEvent(msg.event)
      } catch {}
    }
    return () => es.close()
  }, [applyEvent])

  const leaseArr  = Object.values(leases)
  const active    = leaseArr.filter(l => l.state === 'bound' || l.state === 'offered').length
  const reserved  = leaseArr.filter(l => l.state === 'reserved').length
  const declined  = leaseArr.filter(l => l.state === 'declined').length
  const totalIPs  = subnets.reduce((s, sn) => s + sn.total, 0)
  const avail     = Math.max(0, totalIPs - leaseArr.length)

  return (
    <div className="app">
      <header className="header">
        <div className="header-title">
          <span className="logo">◈</span> mydhcp dashboard
        </div>
        <div className="header-status">
          <span className={`dot ${connected ? 'dot-live' : 'dot-off'}`} />
          <span className={connected ? 'live-text' : 'off-text'}>
            {connected ? 'Live' : 'Disconnected'}
          </span>
          <span className="sep">·</span>
          <span className="muted">{totalEvents.toLocaleString()} events</span>
        </div>
      </header>

      <div className="stats-row">
        <StatCard label="Available" value={avail}    color="#3b82f6" />
        <StatCard label="Active"    value={active}   color="#22c55e" />
        <StatCard label="Reserved"  value={reserved} color="#a78bfa" />
        <StatCard label="Declined"  value={declined} color="#ef4444" />
      </div>

      <div className="main-grid">
        <EventFeed events={events} />
        <SubnetGrid subnets={subnets} leases={leases} />
      </div>

      <LeaseTable leases={leases} tick={tick} />
    </div>
  )
}
