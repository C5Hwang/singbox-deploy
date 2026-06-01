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
	LimitBytes       uint64
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
	UsedBytes      uint64        `json:"usedBytes"`
	RemainingBytes uint64        `json:"remainingBytes"`
	LimitBytes     uint64        `json:"limitBytes"`
	ResetTime      string        `json:"resetTime"`
	Trend          []HourlyPoint `json:"trend"`
}

func (m *Monitor) handleSummary(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	used, err := m.usedThisCycle(now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := Quota{LimitBytes: m.cfg.LimitBytes, UsedBytes: used, ResetDay: m.cfg.ResetDay}
	trend, err := m.store.TrendHourly(now.Add(-historyRetention).Unix())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary{
		UsedBytes:      used,
		RemainingBytes: q.Remaining(),
		LimitBytes:     m.cfg.LimitBytes,
		ResetTime:      NextCycleReset(now, m.cfg.ResetDay).Format(time.RFC3339),
		Trend:          trend,
	})
}

func (m *Monitor) usedThisCycle(now time.Time) (uint64, error) {
	return m.store.TotalSince(CycleStart(now, m.cfg.ResetDay).Unix())
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
	var delta uint64
	if m.havePrev {
		delta = Delta(m.prev.RXBytes, cur.RXBytes) + Delta(m.prev.TXBytes, cur.TXBytes)
	}
	m.prev, m.havePrev = cur, true
	if err := m.store.InsertSample(now.Unix(), cur.Name, cur.RXBytes, cur.TXBytes, delta); err != nil {
		log.Printf("monitor: insert sample: %v", err)
		return
	}
	m.enforceQuota(now)
}

// enforceQuota stops sing-box when over quota and restarts it after a reset.
func (m *Monitor) enforceQuota(now time.Time) {
	if m.control == nil || m.cfg.LimitBytes == 0 {
		return
	}
	used, err := m.usedThisCycle(now)
	if err != nil {
		log.Printf("monitor: used this cycle: %v", err)
		return
	}
	q := Quota{LimitBytes: m.cfg.LimitBytes, UsedBytes: used}
	switch {
	case q.Exceeded():
		if active, _ := m.control.IsActive(); active {
			if err := m.control.Stop(); err != nil {
				log.Printf("monitor: stop sing-box: %v", err)
				return
			}
			m.stoppedByQuota = true
			log.Printf("monitor: quota exceeded (%d/%d bytes), stopped sing-box", used, m.cfg.LimitBytes)
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
