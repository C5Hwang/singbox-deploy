package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
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
)

// ServiceController starts/stops sing-box for quota enforcement.
type ServiceController interface {
	Start() error
	Stop() error
	IsActive() (bool, error)
}

// Config configures the monitor service.
type Config struct {
	Listen               string
	Interface            string
	SamplingInterval     time.Duration
	InLimitBytes         uint64
	OutLimitBytes        uint64
	TotalLimitBytes      uint64
	ResetDay             int
	ResetHour            int
	Alias                string
	RemoteMonitorPath    string
	LocalPositionPath    string
	RefreshRemoteSources func(context.Context) error
	// FetchRemoteData retrieves one fixed drill-down resource for a stable
	// source ID. The hub wires this to its authenticated spoke-agent API; the
	// browser never receives overlay addresses or bearer tokens.
	FetchRemoteData func(context.Context, string, string) ([]byte, error)
	Now             func() time.Time
}

// Monitor samples interface counters, enforces the quota, and serves the API/UI.
type Monitor struct {
	store   *Store
	cfg     Config
	control ServiceController

	prev           InterfaceCounters
	havePrev       bool
	stoppedByQuota bool

	resCollector *ResourceCollector
	// latestResource is written by the sampling goroutine and read by HTTP
	// handlers, hence the atomic pointer.
	latestResource  atomic.Pointer[ResourceSnapshot]
	remoteRefreshMu sync.Mutex
}

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
		store:        store,
		cfg:          cfg,
		control:      control,
		resCollector: NewResourceCollector("/"),
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
	mux := http.NewServeMux()
	mux.HandleFunc("/api/summary", m.handleSummary)
	mux.HandleFunc("/api/traffic-trend", m.handleTrafficTrend)
	mux.HandleFunc("/api/traffic-recent", m.handleTrafficRecent)
	mux.HandleFunc("/api/resource-trend", m.handleResourceTrend)
	mux.HandleFunc("/api/resource-recent", m.handleResourceRecent)
	if sub, err := fs.Sub(assets.FS, "monitor-ui"); err == nil {
		mux.Handle("/", http.FileServer(http.FS(sub)))
	}
	return mux
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
	used, err := m.usedThisCycle(now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	})
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

func sourceQuery(r *http.Request) string { return r.URL.Query().Get("source") }

// sourceEndpoint parameterizes the trend/recent handlers, which otherwise
// duplicated the same local-vs-remote dispatch.
type sourceEndpoint struct {
	key       string                          // JSON response field ("trend" or "points")
	proxyPath string                          // remote API path to proxy to
	local     func() (any, error)             // local data provider
	embedded  func(SourceSummary) (any, bool) // optional embedded-snapshot fallback
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
			body, err := m.cfg.FetchRemoteData(ctx, rs.ID, ep.proxyPath)
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

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sampleTicker.C:
				m.sampleOnce(m.now())
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

func (m *Monitor) enforceQuota(now time.Time) {
	if m.control == nil {
		return
	}
	limits := TrafficLimits{InBytes: m.cfg.InLimitBytes, OutBytes: m.cfg.OutLimitBytes, TotalBytes: m.cfg.TotalLimitBytes}
	if limits == (TrafficLimits{}) {
		if m.stoppedByQuota {
			if err := m.control.Start(); err != nil {
				log.Printf("monitor: start sing-box: %v", err)
				return
			}
			m.setStoppedByQuota(false)
			log.Printf("monitor: traffic limits removed, restarted sing-box")
		}
		return
	}
	used, err := m.usedThisCycle(now)
	if err != nil {
		log.Printf("monitor: used this cycle: %v", err)
		return
	}
	switch {
	case limits.Exceeded(used):
		if active, _ := m.control.IsActive(); active {
			if err := m.control.Stop(); err != nil {
				log.Printf("monitor: stop sing-box: %v", err)
				return
			}
			m.setStoppedByQuota(true)
			log.Printf("monitor: quota exceeded (in=%d/%d out=%d/%d total=%d/%d bytes), stopped sing-box", used.InBytes, m.cfg.InLimitBytes, used.OutBytes, m.cfg.OutLimitBytes, used.Total(), m.cfg.TotalLimitBytes)
		}
	case m.stoppedByQuota:
		if err := m.control.Start(); err != nil {
			log.Printf("monitor: start sing-box: %v", err)
			return
		}
		m.setStoppedByQuota(false)
		log.Printf("monitor: new cycle, restarted sing-box")
	}
}

// setStoppedByQuota updates the in-memory flag and persists it; a write failure
// is logged but does not block quota enforcement.
func (m *Monitor) setStoppedByQuota(stopped bool) {
	m.stoppedByQuota = stopped
	if err := m.store.SetQuotaStopped(stopped); err != nil {
		log.Printf("monitor: persist quota stop flag: %v", err)
	}
}

func (m *Monitor) maintenance(now time.Time) {
	if err := m.store.AggregateHourly(now.Add(-rawRetention).Unix()); err != nil {
		log.Printf("monitor: aggregate: %v", err)
	}
	if err := m.store.AggregateResourceHourly(now.Add(-resourceRawRetention).Unix()); err != nil {
		log.Printf("monitor: aggregate resources: %v", err)
	}
	if err := m.store.Cleanup(now.Add(-historyRetention).Unix()); err != nil {
		log.Printf("monitor: cleanup: %v", err)
	}
}
