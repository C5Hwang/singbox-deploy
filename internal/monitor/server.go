package monitor

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/C5Hwang/singbox-deploy/assets"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

const (
	DefaultSamplingInterval = 1 * time.Minute
	DefaultResourceInterval = 10 * time.Second
	remoteRefreshInterval   = 30 * time.Second
	rawRetention            = 2 * time.Hour
	resourceRawRetention    = 2 * time.Hour
	historyRetention        = 90 * 24 * time.Hour
	// How long per-address history stays readable at hour granularity before it
	// folds into days. A week covers every window the dashboard draws hourly and
	// leaves the rest of a quota cycle to the cheaper tier.
	ipHourlyRetention = 7 * 24 * time.Hour
)

// ServiceController starts/stops sing-box for quota enforcement.
type ServiceController interface {
	Start() error
	Stop() error
	IsActive() (bool, error)
}

// Config configures the monitor service.
type Config struct {
	Listen            string
	Interface         string
	SamplingInterval  time.Duration
	InLimitBytes      uint64
	OutLimitBytes     uint64
	TotalLimitBytes   uint64
	ResetDay          int
	ResetHour         int
	Alias             string
	RemoteMonitorPath string
	LocalPositionPath string
	// AccessToken guards the JSON API. An empty token leaves the API open,
	// which is the state every installation made before the token existed is
	// in; those keep working until an operator sets one.
	AccessToken          string
	RefreshRemoteSources func(context.Context) error
	// FetchRemoteData retrieves one fixed drill-down resource for a stable
	// source ID. The hub wires this to its authenticated spoke-agent API; the
	// browser never receives overlay addresses or bearer tokens. The values are
	// this process's own, not the caller's.
	FetchRemoteData func(ctx context.Context, sourceID, path string, query url.Values) ([]byte, error)
	// ExtraPingTargets contributes latency probe destinations beyond the fixed
	// carrier list — on a relay, one per landing node it fronts. It is called
	// once per round, so a link added or withdrawn is picked up without a
	// restart.
	ExtraPingTargets func() []PingTarget
	// RelayForwards reports the port mappings this node's relay data plane
	// answers on. The relay forwards with a DNAT, which moves those flows onto
	// the forward path where the per-IP peer counters never see them; the ports
	// are what lets the accounting meter them anyway, and the landing node
	// behind each port is what lets a client's forwarded bytes be reported per
	// destination. It is read once per sample round like ExtraPingTargets; nil
	// stands for a node that never relays.
	RelayForwards func() []RelayForward
	// RelayRegistry reports the fleet's relay topology: the dashboard source IDs
	// of the nodes that front another node, and how many nodes are fronted in
	// total. Only the hub has a relay registry to answer from; a spoke leaves it
	// nil, and its dashboard simply never offers the relay page.
	RelayRegistry func() (relays []string, links int)
	Now           func() time.Time
}

// Monitor samples interface counters, enforces the quota, and serves the API/UI.
type Monitor struct {
	store   *Store
	cfg     Config
	control ServiceController

	prev           InterfaceCounters
	havePrev       bool
	stoppedByQuota bool
	// trafficMu linearizes sampling, absolute usage adjustments, and quota
	// enforcement. In particular, a sample cannot land between calculating an
	// adjustment and committing it.
	trafficMu sync.Mutex

	resCollector *ResourceCollector
	// pingCollector is nil on a host with no ping utility, which disables
	// latency sampling without affecting any other metric.
	pingCollector *PingCollector
	// ipAccounting is nil on a host with no nft utility, which disables the
	// per-address counters the same way.
	ipAccounting *IPAccounting
	// latestResource is written by the sampling goroutine and read by HTTP
	// handlers, hence the atomic pointer.
	latestResource  atomic.Pointer[ResourceSnapshot]
	remoteRefreshMu sync.Mutex
}

// TrafficUsage is one authoritative current-cycle usage snapshot.
type TrafficUsage struct {
	Totals     TrafficTotals
	CycleStart time.Time
}

// TrafficUsageUpdate records the exact totals immediately before and after an
// absolute adjustment.
type TrafficUsageUpdate struct {
	Previous TrafficUsage
	Applied  TrafficUsage
	Warning  string
}

// ErrTrafficCycleChanged indicates that an absolute adjustment was prepared
// for a quota cycle that has since reset.
var ErrTrafficCycleChanged = errors.New("traffic quota cycle changed")

// New returns a Monitor backed by store. control may be nil to disable quota
// enforcement (e.g. in tests).
func New(store *Store, cfg Config, control ServiceController) *Monitor {
	if cfg.SamplingInterval <= 0 {
		cfg.SamplingInterval = DefaultSamplingInterval
	}
	if cfg.ResetDay < 1 {
		cfg.ResetDay = 1
	}
	if cfg.ResetHour < 0 || cfg.ResetHour > 23 {
		cfg.ResetHour = 0
	}
	m := &Monitor{
		store:         store,
		cfg:           cfg,
		control:       control,
		resCollector:  NewResourceCollector("/"),
		pingCollector: newPingCollectorWithExtra(cfg.ExtraPingTargets),
		ipAccounting:  NewIPAccounting(cfg.RelayForwards),
	}
	if m.ipAccounting == nil {
		log.Printf("monitor: no nft utility found; per-IP traffic accounting is disabled")
	}
	// Restore the quota-stop flag so a monitor restart (settings change, host
	// reboot) cannot strand sing-box in the stopped state forever.
	if stopped, err := store.QuotaStopped(); err != nil {
		log.Printf("monitor: read quota stop flag: %v", err)
	} else {
		m.stoppedByQuota = stopped
	}
	return m
}

// Handler returns the HTTP handler exposing the API and the embedded UI.
func (m *Monitor) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("/api/summary", m.handleSummary)
	api.HandleFunc("/api/traffic-trend", m.handleTrafficTrend)
	api.HandleFunc("/api/traffic-recent", m.handleTrafficRecent)
	api.HandleFunc("/api/resource-trend", m.handleResourceTrend)
	api.HandleFunc("/api/resource-recent", m.handleResourceRecent)
	api.HandleFunc("/api/ping-trend", m.handlePingTrend)
	api.HandleFunc("/api/ping-series", m.handlePingSeries)
	api.HandleFunc("/api/ip-traffic", m.handleIPTraffic)
	api.HandleFunc("/api/ip-detail", m.handleIPDetail)

	mux := http.NewServeMux()
	// Only the JSON API is guarded. The static bundle has to load unauthenticated
	// so the dashboard can ask for the token in the first place.
	mux.Handle("/api/", m.authorizeAPI(api))
	if sub, err := fs.Sub(assets.FS, "monitor-ui"); err == nil {
		mux.Handle("/", http.FileServer(http.FS(sub)))
	}
	return mux
}

// authorizeAPI rejects API requests that do not carry the configured access
// token.
func (m *Monitor) authorizeAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := m.cfg.AccessToken
		if want != "" && subtle.ConstantTimeCompare([]byte(requestAccessToken(r)), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="monitor"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestAccessToken reads the token from the Authorization header, falling
// back to X-Monitor-Token. The query string is deliberately not consulted: it
// would land in Nginx access logs, browser history, and Referer headers.
func requestAccessToken(r *http.Request) string {
	if values := r.Header.Values("Authorization"); len(values) == 1 {
		if token, ok := strings.CutPrefix(values[0], "Bearer "); ok {
			return token
		}
	}
	return r.Header.Get("X-Monitor-Token")
}

// summary is the JSON payload returned by /api/summary.
type summary struct {
	InUsedBytes         uint64            `json:"inUsedBytes"`
	OutUsedBytes        uint64            `json:"outUsedBytes"`
	TotalUsedBytes      uint64            `json:"totalUsedBytes"`
	InRemainingBytes    uint64            `json:"inRemainingBytes"`
	OutRemainingBytes   uint64            `json:"outRemainingBytes"`
	TotalRemainingBytes uint64            `json:"totalRemainingBytes"`
	InLimitBytes        uint64            `json:"inLimitBytes"`
	OutLimitBytes       uint64            `json:"outLimitBytes"`
	TotalLimitBytes     uint64            `json:"totalLimitBytes"`
	ResetTime           string            `json:"resetTime"`
	Resources           *ResourceSnapshot `json:"resources,omitempty"`
	Sources             []SourceSummary   `json:"sources"`
	// RelayLinks is how many nodes in the fleet are fronted by a relay. The
	// dashboard uses it to decide whether the relay page is worth offering at
	// all, which it cannot infer from the sources: whether a node relays is a
	// fact about the hub's registry, not about any node's traffic.
	RelayLinks int `json:"relayLinks"`
	// RelayNodes names the sources that do the relaying, so the relay page asks
	// those and only those for their probes. Without it the page has to ask
	// every node and discard the ones that answer with no relay targets, which
	// makes an unrelated node that is down slow down a page it has nothing to
	// do with.
	RelayNodes []string `json:"relayNodes,omitempty"`
}

// SourceSummary is one traffic source shown by the monitor UI.
type SourceSummary struct {
	ID                  string                `json:"id"`
	Name                string                `json:"name"`
	FetchedAt           string                `json:"fetchedAt,omitempty"`
	SampledAt           string                `json:"sampledAt,omitempty"`
	MonitorURL          string                `json:"monitorURL,omitempty"`
	InUsedBytes         uint64                `json:"inUsedBytes"`
	OutUsedBytes        uint64                `json:"outUsedBytes"`
	TotalUsedBytes      uint64                `json:"totalUsedBytes"`
	InRemainingBytes    uint64                `json:"inRemainingBytes"`
	OutRemainingBytes   uint64                `json:"outRemainingBytes"`
	TotalRemainingBytes uint64                `json:"totalRemainingBytes"`
	InLimitBytes        uint64                `json:"inLimitBytes"`
	OutLimitBytes       uint64                `json:"outLimitBytes"`
	TotalLimitBytes     uint64                `json:"totalLimitBytes"`
	ResetTime           string                `json:"resetTime"`
	Trend               []HourlyPoint         `json:"trend,omitempty"`
	Resources           *ResourceSnapshot     `json:"resources,omitempty"`
	ResourceTrend       []ResourceHourlyPoint `json:"resourceTrend,omitempty"`
}

func (m *Monitor) handleSummary(w http.ResponseWriter, r *http.Request) {
	now := m.now()
	usage, err := m.CurrentTrafficUsage()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	used := usage.Totals
	var sampledAt string
	if ts, ok := m.store.LatestSampleTime(); ok {
		sampledAt = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	local := SourceSummary{
		ID:                  "local",
		Name:                m.localAlias(),
		SampledAt:           sampledAt,
		InUsedBytes:         used.InBytes,
		OutUsedBytes:        used.OutBytes,
		TotalUsedBytes:      used.Total(),
		InRemainingBytes:    Remaining(m.cfg.InLimitBytes, used.InBytes),
		OutRemainingBytes:   Remaining(m.cfg.OutLimitBytes, used.OutBytes),
		TotalRemainingBytes: Remaining(m.cfg.TotalLimitBytes, used.Total()),
		InLimitBytes:        m.cfg.InLimitBytes,
		OutLimitBytes:       m.cfg.OutLimitBytes,
		TotalLimitBytes:     m.cfg.TotalLimitBytes,
		ResetTime:           NextCycleReset(now, m.cfg.ResetDay, m.cfg.ResetHour).Format(time.RFC3339),
		Resources:           m.latestResource.Load(),
	}
	remote, err := ReadRemoteSources(m.cfg.RemoteMonitorPath)
	if err != nil {
		log.Printf("monitor: read remote monitor data: %v", err)
	}
	sources := insertLocalSource(remote, readLocalPositionFile(m.cfg.LocalPositionPath), local)
	for i := range sources {
		sources[i].MonitorURL = ""
	}
	relayNodes, relayLinks := m.relayRegistry()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary{
		InUsedBytes:         local.InUsedBytes,
		OutUsedBytes:        local.OutUsedBytes,
		TotalUsedBytes:      local.TotalUsedBytes,
		InRemainingBytes:    local.InRemainingBytes,
		OutRemainingBytes:   local.OutRemainingBytes,
		TotalRemainingBytes: local.TotalRemainingBytes,
		InLimitBytes:        local.InLimitBytes,
		OutLimitBytes:       local.OutLimitBytes,
		TotalLimitBytes:     local.TotalLimitBytes,
		ResetTime:           local.ResetTime,
		Resources:           local.Resources,
		Sources:             sources,
		RelayLinks:          relayLinks,
		RelayNodes:          relayNodes,
	})
}

// relayRegistry reports which sources relay and how many nodes are relayed, or
// nothing on a deployment that has no registry to ask — a spoke's own monitor,
// or one built before relaying existed.
func (m *Monitor) relayRegistry() ([]string, int) {
	if m.cfg.RelayRegistry == nil {
		return nil, 0
	}
	return m.cfg.RelayRegistry()
}

func (m *Monitor) refreshRemoteSources(ctx context.Context) {
	if m.cfg.RefreshRemoteSources == nil {
		return
	}
	m.remoteRefreshMu.Lock()
	defer m.remoteRefreshMu.Unlock()
	if err := m.cfg.RefreshRemoteSources(ctx); err != nil {
		log.Printf("monitor: refresh remote monitor data: %v", err)
	}
}

func (m *Monitor) handleTrafficTrend(w http.ResponseWriter, r *http.Request) {
	now := m.now()
	m.serveSourceData(r.Context(), w, sourceQuery(r), sourceEndpoint{
		key:       "trend",
		proxyPath: "/api/traffic-trend",
		local:     func() (any, error) { return m.store.TrendHourly(now.Add(-historyRetention).Unix()) },
		embedded:  func(rs SourceSummary) (any, bool) { return rs.Trend, true },
	})
}

func (m *Monitor) handleResourceTrend(w http.ResponseWriter, r *http.Request) {
	now := m.now()
	m.serveSourceData(r.Context(), w, sourceQuery(r), sourceEndpoint{
		key:       "trend",
		proxyPath: "/api/resource-trend",
		local: func() (any, error) {
			trend, err := m.store.ResourceTrendHourly(now.Add(-historyRetention).Unix())
			if err != nil {
				return nil, err
			}
			return resourceTrendToRates(trend), nil
		},
		embedded: func(rs SourceSummary) (any, bool) { return rs.ResourceTrend, true },
	})
}

func (m *Monitor) handleTrafficRecent(w http.ResponseWriter, r *http.Request) {
	now := m.now()
	m.serveSourceData(r.Context(), w, sourceQuery(r), sourceEndpoint{
		key:       "points",
		proxyPath: "/api/traffic-recent",
		local:     func() (any, error) { return m.store.TrafficRawSamples(now.Add(-rawRetention).Unix()) },
	})
}

func (m *Monitor) handleResourceRecent(w http.ResponseWriter, r *http.Request) {
	now := m.now()
	m.serveSourceData(r.Context(), w, sourceQuery(r), sourceEndpoint{
		key:       "points",
		proxyPath: "/api/resource-recent",
		local: func() (any, error) {
			points, err := m.store.ResourceRawSamples(now.Add(-resourceRawRetention).Unix())
			if err != nil {
				return nil, err
			}
			return resourceRecentToRates(points), nil
		},
	})
}

func (m *Monitor) handlePingTrend(w http.ResponseWriter, r *http.Request) {
	since := m.now().Add(-pingRetention).Unix()
	m.serveSourceData(r.Context(), w, sourceQuery(r), sourceEndpoint{
		key:       "latency",
		proxyPath: "/api/ping-trend",
		local:     func() (any, error) { return m.latencySnapshot(since) },
	})
}

// handlePingSeries serves the week of one-minute rounds behind the trend chart.
// It is deliberately not part of the snapshot the page polls: the history only
// changes by one slot a minute, so re-sending all of it every minute would be
// nearly a megabyte to say what one number already said.
func (m *Monitor) handlePingSeries(w http.ResponseWriter, r *http.Request) {
	now := m.now()
	since := now.Add(-pingRetention).Unix()
	m.serveSourceData(r.Context(), w, sourceQuery(r), sourceEndpoint{
		key:       "series",
		proxyPath: "/api/ping-series",
		local: func() (any, error) {
			return m.store.PingSeriesData(since, now.Unix(), int64(PingInterval/time.Second))
		},
	})
}

func (m *Monitor) latencySnapshot(since int64) (LatencySnapshot, error) {
	latest, err := m.store.LatestPingSamples(since)
	if err != nil {
		return LatencySnapshot{}, err
	}
	return LatencySnapshot{Targets: m.pingCollector.Targets(), Latest: latest}, nil
}

// topIPTrafficEntries bounds one response, counted in stored keys rather than
// in addresses because that is what a row is. It exists so a table that has not
// been pruned yet cannot produce an unbounded response, and it sits well clear
// of what a pruned one comes to: ipTrafficKeptAddresses addresses, each holding
// its direct traffic plus a key for every landing node a relay carried it to.
// The dashboard pages through whatever comes back rather than reading a top-N,
// which is also what makes merging several nodes' lists exact instead of
// approximate.
const topIPTrafficEntries = 4000

// ipTrafficKeptAddresses bounds the per-IP table between prunes, counted in
// addresses: a client the relay carried holds one stored key per landing node
// beside its direct one, and the budget is for clients, not for strands.
const ipTrafficKeptAddresses = 500

func (m *Monitor) handleIPTraffic(w http.ResponseWriter, r *http.Request) {
	now := m.now()
	cycleStart := CycleStart(now, m.cfg.ResetDay, m.cfg.ResetHour).Unix()
	// The two shorter windows are GMT-aligned, matching the quota cycle the
	// rest of the dashboard is keyed to rather than the viewer's timezone.
	todayStart := now.Truncate(24 * time.Hour).Unix()
	weekStart := todayStart - 6*86400
	m.serveSourceData(r.Context(), w, sourceQuery(r), sourceEndpoint{
		key:       "ipTraffic",
		proxyPath: "/api/ip-traffic",
		local: func() (any, error) {
			entries, err := m.store.TopIPTraffic(topIPTrafficEntries, cycleStart, todayStart, weekStart)
			if err != nil {
				return nil, err
			}
			names := m.relayLandingNames()
			for i := range entries {
				entries[i].LandingName = names[entries[i].Landing]
			}
			return IPTrafficSnapshot{
				Enabled:    m.ipAccounting.Enabled(),
				CycleStart: cycleStart,
				Entries:    entries,
			}, nil
		},
	})
}

// handleIPDetail serves one accounting key's history: an address, optionally
// behind the relay marker. The key is parsed before it reaches the store, so
// the only thing that ever reaches a query is a value this process
// re-serialized.
func (m *Monitor) handleIPDetail(w http.ResponseWriter, r *http.Request) {
	key, err := ParseIPKey(r.URL.Query().Get("ip"))
	if err != nil {
		http.Error(w, "ip must be an IP address", http.StatusBadRequest)
		return
	}
	now := m.now()
	cycleStart := CycleStart(now, m.cfg.ResetDay, m.cfg.ResetHour).Unix()
	recentSince := now.Add(-rawRetention).Unix()
	m.serveSourceData(r.Context(), w, sourceQuery(r), sourceEndpoint{
		key:        "ipDetail",
		proxyPath:  "/api/ip-detail",
		proxyQuery: url.Values{"ip": []string{key}},
		local: func() (any, error) {
			detail, err := m.store.IPTrafficSeries(key, recentSince, cycleStart)
			if err != nil {
				return nil, err
			}
			detail.LandingName = m.relayLandingNames()[detail.Landing]
			return detail, nil
		},
	})
}

// relayLandingNames labels each landing node this relay fronts. A history whose
// landing node has since been un-fronted keeps its ID and loses its name, which
// is the honest reading: the bytes were forwarded, and the relay no longer has
// anything to say about where.
func (m *Monitor) relayLandingNames() map[string]string {
	if m.cfg.RelayForwards == nil {
		return nil
	}
	forwards := m.cfg.RelayForwards()
	names := make(map[string]string, len(forwards))
	for _, forward := range forwards {
		if forward.LandingID == "" || forward.LandingName == "" {
			continue
		}
		names[forward.LandingID] = forward.LandingName
	}
	return names
}

func sourceQuery(r *http.Request) string { return r.URL.Query().Get("source") }

// sourceEndpoint parameterizes the trend/recent handlers, so the
// local-vs-remote dispatch is written once for every resource that has both
// readings.
type sourceEndpoint struct {
	key       string // JSON response field ("trend" or "points")
	proxyPath string // remote API path to proxy to
	// proxyQuery carries the parameters a remote read needs. Every value in it
	// has already been parsed and re-serialized by this process, so nothing a
	// caller typed is forwarded verbatim.
	proxyQuery url.Values
	local      func() (any, error)             // local data provider
	embedded   func(SourceSummary) (any, bool) // optional embedded-snapshot fallback
}

func (m *Monitor) serveSourceData(ctx context.Context, w http.ResponseWriter, source string, ep sourceEndpoint) {
	if source == "" || source == "local" || source == m.localAlias() {
		data, err := ep.local()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{ep.key: data})
		return
	}
	remotes, _ := ReadRemoteSources(m.cfg.RemoteMonitorPath)
	for _, rs := range remotes {
		if rs.ID != source && rs.Name != source {
			continue
		}
		if m.cfg.FetchRemoteData != nil {
			if rs.ID == "" {
				http.Error(w, "remote source is not a managed spoke", http.StatusNotFound)
				return
			}
			body, err := m.cfg.FetchRemoteData(ctx, rs.ID, ep.proxyPath, ep.proxyQuery)
			if err != nil {
				http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		if rs.MonitorURL != "" {
			m.proxyRemote(w, rs.MonitorURL+ep.proxyPath)
			return
		}
		if ep.embedded != nil {
			if data, ok := ep.embedded(rs); ok {
				writeJSON(w, map[string]any{ep.key: data})
				return
			}
		}
		break
	}
	http.Error(w, "source not found", http.StatusNotFound)
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// The store keeps disk IO as raw per-interval byte deltas; the UI displays
// bytes/sec. Convert at the API boundary so stored history stays compatible.
func resourceTrendToRates(points []ResourceHourlyPoint) []ResourceHourlyPoint {
	sec := int64(DefaultResourceInterval / time.Second)
	for i := range points {
		points[i].DIOReadAvg /= sec
		points[i].DIOReadMax /= sec
		points[i].DIOWriteAvg /= sec
		points[i].DIOWriteMax /= sec
	}
	return points
}

func resourceRecentToRates(points []ResourceRawPoint) []ResourceRawPoint {
	sec := int64(DefaultResourceInterval / time.Second)
	for i := range points {
		points[i].DIORead /= sec
		points[i].DIOWrite /= sec
	}
	return points
}

func (m *Monitor) proxyRemote(w http.ResponseWriter, url string) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		http.Error(w, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// ReadRemoteSources reads remote monitor snapshots.
func ReadRemoteSources(path string) ([]SourceSummary, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sources []SourceSummary
	if err := json.Unmarshal(b, &sources); err != nil {
		return nil, err
	}
	return sources, nil
}

// WriteRemoteSources writes remote monitor snapshots for the monitor API. The
// write is atomic: the TUI process and monitor service read this file while
// the background refresher rewrites it.
func WriteRemoteSources(path string, sources []SourceSummary) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sources, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(path, b, 0o600)
}

func readLocalPositionFile(path string) int {
	if path == "" {
		return 0
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func insertLocalSource(remote []SourceSummary, pos int, local SourceSummary) []SourceSummary {
	if pos < 0 {
		pos = 0
	}
	if pos > len(remote) {
		pos = len(remote)
	}
	sources := make([]SourceSummary, 0, 1+len(remote))
	for i, r := range remote {
		if i == pos {
			sources = append(sources, local)
		}
		sources = append(sources, r)
	}
	if pos >= len(remote) {
		sources = append(sources, local)
	}
	return sources
}

func (m *Monitor) usedThisCycle(now time.Time) (TrafficTotals, error) {
	return m.store.TotalsSince(CycleStart(now, m.cfg.ResetDay, m.cfg.ResetHour).Unix())
}

// CurrentTrafficUsage returns a linearized snapshot for the active quota
// cycle.
func (m *Monitor) CurrentTrafficUsage() (TrafficUsage, error) {
	m.trafficMu.Lock()
	defer m.trafficMu.Unlock()
	now := m.now()
	cycleStart := CycleStart(now, m.cfg.ResetDay, m.cfg.ResetHour)
	totals, err := m.store.TotalsSince(cycleStart.Unix())
	if err != nil {
		return TrafficUsage{}, err
	}
	return TrafficUsage{Totals: totals, CycleStart: cycleStart}, nil
}

// SetCurrentTrafficUsage replaces the absolute totals for the expected active
// quota cycle and immediately reconciles quota service state.
func (m *Monitor) SetCurrentTrafficUsage(expectedCycleStart int64, target TrafficTotals) (TrafficUsageUpdate, error) {
	m.trafficMu.Lock()
	defer m.trafficMu.Unlock()
	now := m.now()
	cycleStart := CycleStart(now, m.cfg.ResetDay, m.cfg.ResetHour)
	if cycleStart.Unix() != expectedCycleStart {
		return TrafficUsageUpdate{}, ErrTrafficCycleChanged
	}
	previous, err := m.store.ReplaceTotalsSince(cycleStart.Unix(), now.Unix(), target)
	if err != nil {
		return TrafficUsageUpdate{}, err
	}
	update := TrafficUsageUpdate{
		Previous: TrafficUsage{Totals: previous, CycleStart: cycleStart},
		Applied:  TrafficUsage{Totals: target, CycleStart: cycleStart},
	}
	if err := m.reconcileQuota(now); err != nil {
		update.Warning = trafficUsageReconciliationWarning(err)
	}
	return update, nil
}

const maxTrafficUsageWarningBytes = 2048

func trafficUsageReconciliationWarning(err error) string {
	warning := strings.ToValidUTF8(
		fmt.Sprintf("traffic usage was updated but quota reconciliation failed: %v", err),
		"\uFFFD",
	)
	warning = strings.NewReplacer("\r", " ", "\n", " ").Replace(warning)
	if len(warning) <= maxTrafficUsageWarningBytes {
		return warning
	}
	const suffix = "..."
	cut := maxTrafficUsageWarningBytes - len(suffix)
	for cut > 0 && cut < len(warning) && warning[cut]&0xc0 == 0x80 {
		cut--
	}
	return strings.TrimSpace(warning[:cut]) + suffix
}

func (m *Monitor) now() time.Time {
	if m.cfg.Now != nil {
		return m.cfg.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Monitor) localAlias() string {
	alias := m.cfg.Alias
	if alias == "" {
		alias = "Local Server"
	}
	return subscription.AddNodePrefixFlag(alias)
}

// Run starts the sampling loop and HTTP server until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	srv := &http.Server{Addr: m.cfg.Listen, Handler: m.Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	sampleTicker := time.NewTicker(m.cfg.SamplingInterval)
	defer sampleTicker.Stop()
	resourceTicker := time.NewTicker(DefaultResourceInterval)
	defer resourceTicker.Stop()
	maintTicker := time.NewTicker(time.Hour)
	defer maintTicker.Stop()

	if rx, tx, ok, _ := m.store.LatestCounters(m.cfg.Interface); ok {
		m.prev = InterfaceCounters{Name: m.cfg.Interface, RXBytes: rx, TXBytes: tx}
		m.havePrev = true
	}
	m.sampleOnce(m.now())
	m.resourceSampleOnce(m.now())
	m.ipSampleOnce(ctx, m.now())
	// The accounting table is registered on this host's netfilter hooks, so a
	// monitor that is shutting down takes it with it.
	defer func() {
		removeCtx, cancel := context.WithTimeout(context.Background(), ipAcctCommandTimeout)
		defer cancel()
		if err := m.ipAccounting.Remove(removeCtx); err != nil {
			log.Printf("monitor: remove per-IP accounting ruleset: %v", err)
		}
	}()

	// Remote sources are refreshed in the background: request handlers only
	// read the snapshot file, so a slow or dead peer (or two nodes monitoring
	// each other) cannot stall or recurse through the API.
	if m.cfg.RefreshRemoteSources != nil {
		go func() {
			m.refreshRemoteSources(ctx)
			refreshTicker := time.NewTicker(remoteRefreshInterval)
			defer refreshTicker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-refreshTicker.C:
					m.refreshRemoteSources(ctx)
				}
			}
		}()
	}

	// Latency probing gets its own goroutine: one round takes seconds, and
	// running it on the sampling loop would delay traffic samples. Sequencing
	// inside this goroutine is also what stops two rounds from overlapping.
	if m.pingCollector != nil {
		go m.pingLoop(ctx)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sampleTicker.C:
				now := m.now()
				m.sampleOnce(now)
				m.ipSampleOnce(ctx, now)
			case <-resourceTicker.C:
				m.resourceSampleOnce(m.now())
			case <-maintTicker.C:
				m.maintenance(m.now())
			}
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (m *Monitor) sampleOnce(now time.Time) {
	m.trafficMu.Lock()
	defer m.trafficMu.Unlock()
	cur, err := ReadCounters(m.cfg.Interface)
	if err != nil {
		log.Printf("monitor: read counters: %v", err)
		return
	}
	var deltaIn, deltaOut uint64
	if m.havePrev {
		deltaIn = Delta(m.prev.RXBytes, cur.RXBytes)
		deltaOut = Delta(m.prev.TXBytes, cur.TXBytes)
	}
	m.prev, m.havePrev = cur, true
	if err := m.store.InsertSample(now.Unix(), cur.Name, cur.RXBytes, cur.TXBytes, deltaIn, deltaOut); err != nil {
		log.Printf("monitor: insert sample: %v", err)
		return
	}
	m.enforceQuota(now)
}

func (m *Monitor) resourceSampleOnce(now time.Time) {
	reading, err := m.resCollector.Collect()
	if err != nil {
		log.Printf("monitor: resource collect: %v", err)
		return
	}
	if !reading.Valid {
		return
	}
	if err := m.store.InsertResourceSample(
		now.Unix(), reading.CPUPct, reading.MemPct, reading.DiskUsedPct,
		reading.DIOReadDelta, reading.DIOWriteDelta,
	); err != nil {
		log.Printf("monitor: insert resource sample: %v", err)
		return
	}
	intervalSec := DefaultResourceInterval.Seconds()
	m.latestResource.Store(&ResourceSnapshot{
		CPUPct:          reading.CPUPct,
		MemPct:          reading.MemPct,
		MemUsedBytes:    reading.MemUsedBytes,
		MemTotalBytes:   reading.MemTotalBytes,
		DiskUsagePct:    reading.DiskUsedPct,
		DiskUsedBytes:   reading.DiskUsedBytes,
		DiskTotalBytes:  reading.DiskTotalBytes,
		DiskIOReadRate:  float64(reading.DIOReadDelta) / intervalSec,
		DiskIOWriteRate: float64(reading.DIOWriteDelta) / intervalSec,
	})
}

// ipSampleOnce folds one round of nftables counters into the per-IP table,
// clearing it first whenever the quota cycle has rolled over.
func (m *Monitor) ipSampleOnce(ctx context.Context, now time.Time) {
	if !m.ipAccounting.Enabled() {
		return
	}
	cleared, err := m.store.ResetIPTrafficForCycle(CycleStart(now, m.cfg.ResetDay, m.cfg.ResetHour).Unix())
	if err != nil {
		log.Printf("monitor: reset per-IP traffic for the new quota cycle: %v", err)
		return
	}
	if cleared {
		log.Printf("monitor: quota cycle reset, cleared per-IP traffic")
	}
	deltas, err := m.ipAccounting.Collect(ctx)
	if err != nil {
		log.Printf("monitor: collect per-IP traffic: %v", err)
		return
	}
	if err := m.store.AddIPTraffic(now.Unix(), deltas); err != nil {
		log.Printf("monitor: insert per-IP traffic: %v", err)
	}
}

func (m *Monitor) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	m.pingSampleOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pingSampleOnce(ctx)
		}
	}
}

func (m *Monitor) pingSampleOnce(ctx context.Context) {
	samples := m.pingCollector.Collect(ctx)
	if len(samples) == 0 {
		return
	}
	if err := m.store.InsertPingSamples(m.now().Unix(), samples); err != nil {
		log.Printf("monitor: insert ping samples: %v", err)
	}
}

func (m *Monitor) enforceQuota(now time.Time) {
	if err := m.reconcileQuota(now); err != nil {
		log.Printf("monitor: enforce quota: %v", err)
	}
}

func (m *Monitor) reconcileQuota(now time.Time) error {
	if m.control == nil {
		return nil
	}
	limits := TrafficLimits{InBytes: m.cfg.InLimitBytes, OutBytes: m.cfg.OutLimitBytes, TotalBytes: m.cfg.TotalLimitBytes}
	if limits == (TrafficLimits{}) {
		if m.stoppedByQuota {
			if err := m.control.Start(); err != nil {
				return fmt.Errorf("start sing-box after limits were removed: %w", err)
			}
			if err := m.setStoppedByQuota(false); err != nil {
				return fmt.Errorf("clear quota stop ownership after limits were removed: %w", err)
			}
			log.Printf("monitor: traffic limits removed, restarted sing-box")
		}
		return nil
	}
	used, err := m.usedThisCycle(now)
	if err != nil {
		return fmt.Errorf("read usage for quota enforcement: %w", err)
	}
	switch {
	case limits.Exceeded(used):
		active, err := m.control.IsActive()
		if err != nil {
			return fmt.Errorf("inspect sing-box before quota enforcement: %w", err)
		}
		if active {
			// Persist ownership first. If the process dies after this point, a
			// restarted monitor can safely finish or release the stop.
			if err := m.setStoppedByQuota(true); err != nil {
				return fmt.Errorf("persist quota stop ownership: %w", err)
			}
			if err := m.control.Stop(); err != nil {
				return fmt.Errorf("stop sing-box after quota exceeded: %w", err)
			}
			log.Printf("monitor: quota exceeded (in=%d/%d out=%d/%d total=%d/%d bytes), stopped sing-box", used.InBytes, m.cfg.InLimitBytes, used.OutBytes, m.cfg.OutLimitBytes, used.Total(), m.cfg.TotalLimitBytes)
		}
	case m.stoppedByQuota:
		if err := m.control.Start(); err != nil {
			return fmt.Errorf("start sing-box after usage returned below quota: %w", err)
		}
		if err := m.setStoppedByQuota(false); err != nil {
			return fmt.Errorf("clear quota stop ownership after service recovery: %w", err)
		}
		log.Printf("monitor: usage below quota, restarted sing-box")
	}
	return nil
}

// setStoppedByQuota persists ownership before publishing it in memory.
func (m *Monitor) setStoppedByQuota(stopped bool) error {
	if err := m.store.SetQuotaStopped(stopped); err != nil {
		return err
	}
	m.stoppedByQuota = stopped
	return nil
}

func (m *Monitor) maintenance(now time.Time) {
	if err := m.store.AggregateHourly(now.Add(-rawRetention).Unix()); err != nil {
		log.Printf("monitor: aggregate: %v", err)
	}
	if err := m.store.AggregateResourceHourly(now.Add(-resourceRawRetention).Unix()); err != nil {
		log.Printf("monitor: aggregate resources: %v", err)
	}
	// Per-address samples fold on the node's own cutoff, so the two histories
	// always describe the same window at the same granularity.
	if err := m.store.AggregateIPHourly(now.Add(-rawRetention).Unix()); err != nil {
		log.Printf("monitor: aggregate per-IP traffic: %v", err)
	}
	// Then the hours fold again. The node's own hourly table is one row an hour;
	// this one is a row an hour per address, so it is the part that grows enough
	// over a quota cycle to slow the ranking query down. The cutoff is aligned to
	// a GMT day so a day bucket is only written complete and always sits older
	// than the week window handleIPTraffic reads.
	if err := m.store.AggregateIPDaily(now.Truncate(24 * time.Hour).Add(-ipHourlyRetention).Unix()); err != nil {
		log.Printf("monitor: fold per-IP traffic into days: %v", err)
	}
	if err := m.store.Cleanup(now.Add(-historyRetention).Unix()); err != nil {
		log.Printf("monitor: cleanup: %v", err)
	}
	if err := m.store.CleanupPingSamples(now.Add(-pingRetention).Unix()); err != nil {
		log.Printf("monitor: cleanup ping samples: %v", err)
	}
	if err := m.store.PruneIPTraffic(ipTrafficKeptAddresses); err != nil {
		log.Printf("monitor: prune per-IP traffic: %v", err)
	}
}
