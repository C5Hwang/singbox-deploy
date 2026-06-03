package monitor

import (
	"context"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"time"

	assets "github.com/C5Hwang/singbox-deploy/template"
)

// Defaults for sampling and retention.
const (
	DefaultSamplingInterval = 5 * time.Minute
	rawRetention            = 2 * time.Hour
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
	Listen           string
	Interface        string
	SamplingInterval time.Duration
	InLimitBytes     uint64
	OutLimitBytes    uint64
	TotalLimitBytes  uint64
	ResetDay         int
}

// Monitor samples interface counters, enforces the quota, and serves the API/UI.
type Monitor struct {
	store   *Store
	cfg     Config
	control ServiceController

	prev           InterfaceCounters
	havePrev       bool
	stoppedByQuota bool
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
	return &Monitor{store: store, cfg: cfg, control: control}
}

// Handler returns the HTTP handler exposing /api/summary and the embedded UI.
func (m *Monitor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/summary", m.handleSummary)
	if sub, err := fs.Sub(assets.FS, "traffic-ui"); err == nil {
		mux.Handle("/", http.FileServer(http.FS(sub)))
	}
	return mux
}

// summary is the JSON payload returned by /api/summary.
type summary struct {
	InUsedBytes         uint64        `json:"inUsedBytes"`
	OutUsedBytes        uint64        `json:"outUsedBytes"`
	TotalUsedBytes      uint64        `json:"totalUsedBytes"`
	InRemainingBytes    uint64        `json:"inRemainingBytes"`
	OutRemainingBytes   uint64        `json:"outRemainingBytes"`
	TotalRemainingBytes uint64        `json:"totalRemainingBytes"`
	InLimitBytes        uint64        `json:"inLimitBytes"`
	OutLimitBytes       uint64        `json:"outLimitBytes"`
	TotalLimitBytes     uint64        `json:"totalLimitBytes"`
	ResetTime           string        `json:"resetTime"`
	Trend               []HourlyPoint `json:"trend"`
}

func (m *Monitor) handleSummary(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	used, err := m.usedThisCycle(now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	trend, err := m.store.TrendHourly(now.Add(-historyRetention).Unix())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary{
		InUsedBytes:         used.InBytes,
		OutUsedBytes:        used.OutBytes,
		TotalUsedBytes:      used.Total(),
		InRemainingBytes:    Remaining(m.cfg.InLimitBytes, used.InBytes),
		OutRemainingBytes:   Remaining(m.cfg.OutLimitBytes, used.OutBytes),
		TotalRemainingBytes: Remaining(m.cfg.TotalLimitBytes, used.Total()),
		InLimitBytes:        m.cfg.InLimitBytes,
		OutLimitBytes:       m.cfg.OutLimitBytes,
		TotalLimitBytes:     m.cfg.TotalLimitBytes,
		ResetTime:           NextCycleReset(now, m.cfg.ResetDay).Format(time.RFC3339),
		Trend:               trend,
	})
}

func (m *Monitor) usedThisCycle(now time.Time) (TrafficTotals, error) {
	return m.store.TotalsSince(CycleStart(now, m.cfg.ResetDay).Unix())
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
	maintTicker := time.NewTicker(time.Hour)
	defer maintTicker.Stop()

	// Seed previous counters from the store so the first delta is sane across
	// restarts.
	if rx, tx, ok, _ := m.store.LatestCounters(m.cfg.Interface); ok {
		m.prev = InterfaceCounters{Name: m.cfg.Interface, RXBytes: rx, TXBytes: tx}
		m.havePrev = true
	}
	m.sampleOnce(time.Now())

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-sampleTicker.C:
				m.sampleOnce(t)
			case t := <-maintTicker.C:
				m.maintenance(t)
			}
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// sampleOnce reads counters, records the delta, and enforces the quota.
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

// enforceQuota stops sing-box when over quota and restarts it after a reset.
func (m *Monitor) enforceQuota(now time.Time) {
	limits := TrafficLimits{InBytes: m.cfg.InLimitBytes, OutBytes: m.cfg.OutLimitBytes, TotalBytes: m.cfg.TotalLimitBytes}
	if m.control == nil || limits == (TrafficLimits{}) {
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
			m.stoppedByQuota = true
			log.Printf("monitor: quota exceeded (in=%d/%d out=%d/%d total=%d/%d bytes), stopped sing-box", used.InBytes, m.cfg.InLimitBytes, used.OutBytes, m.cfg.OutLimitBytes, used.Total(), m.cfg.TotalLimitBytes)
		}
	case m.stoppedByQuota:
		// New cycle has reduced usage below the limit; restore service.
		if err := m.control.Start(); err != nil {
			log.Printf("monitor: start sing-box: %v", err)
			return
		}
		m.stoppedByQuota = false
		log.Printf("monitor: new cycle, restarted sing-box")
	}
}

// maintenance aggregates raw samples and prunes old history.
func (m *Monitor) maintenance(now time.Time) {
	if err := m.store.AggregateHourly(now.Add(-rawRetention).Unix()); err != nil {
		log.Printf("monitor: aggregate: %v", err)
	}
	if err := m.store.Cleanup(now.Add(-historyRetention).Unix()); err != nil {
		log.Printf("monitor: cleanup: %v", err)
	}
}
