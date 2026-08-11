package monitor

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"

	_ "modernc.org/sqlite"
)

const storeFileMode = 0o600

var sqliteSidecarSuffixes = []string{"-journal", "-wal", "-shm"}

// Store is the SQLite-backed sample store. It is intentionally configured for a
// low-memory (256 MB) VPS: a single connection, a small page cache, and a
// journal mode chosen for low memory over peak write throughput.
type Store struct {
	db *sql.DB
}

// HourlyPoint is one aggregated hourly traffic bucket.
type HourlyPoint struct {
	HourTS     int64 `json:"hourTs"`
	InBytes    int64 `json:"inBytes"`
	OutBytes   int64 `json:"outBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

// TrafficTotals is the in/out traffic used in a quota cycle.
type TrafficTotals struct {
	InBytes  uint64
	OutBytes uint64
}

// Total returns in+out traffic.
func (t TrafficTotals) Total() uint64 { return t.InBytes + t.OutBytes }

// OpenStore opens (creating if needed) the SQLite database at path and applies
// the schema and low-memory pragmas.
func OpenStore(path string) (*Store, error) {
	// SQLite otherwise creates the main database with its default 0644 mode.
	// Secure it before opening so migration journals inherit 0600, and tighten
	// sidecars left by an older version before SQLite has a chance to recover
	// from them.
	if err := secureStorePermissions(path); err != nil {
		return nil, err
	}

	// busy_timeout guards the single connection against transient locks.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(TRUNCATE)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-2000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection only: predictable memory, no writer contention.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	// Migration can create a new rollback journal. SQLite normally copies the
	// main database mode, but enforce the invariant here as well so every file
	// returned by OpenStore is private regardless of driver behavior.
	if err := secureStorePermissions(path); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func secureStorePermissions(path string) error {
	if err := secureStoreFile(path, true); err != nil {
		return fmt.Errorf("secure monitor database: %w", err)
	}
	for _, suffix := range sqliteSidecarSuffixes {
		if err := secureStoreFile(path+suffix, false); err != nil {
			return fmt.Errorf("secure monitor database sidecar %s: %w", suffix, err)
		}
	}
	return nil
}

func secureStoreFile(path string, create bool) error {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, storeFileMode)
	if !create && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := file.Chmod(storeFileMode); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS samples (
    ts          INTEGER NOT NULL,
    iface       TEXT    NOT NULL,
    rx_bytes    INTEGER NOT NULL,
    tx_bytes    INTEGER NOT NULL,
    delta_rx_bytes INTEGER NOT NULL,
    delta_tx_bytes INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples(ts);
CREATE TABLE IF NOT EXISTS hourly (
    ts_hour INTEGER PRIMARY KEY,
    in_bytes INTEGER NOT NULL,
    out_bytes INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS adjustments (
    ts INTEGER NOT NULL,
    in_bytes INTEGER NOT NULL,
    out_bytes INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_adjustments_ts ON adjustments(ts);
CREATE TABLE IF NOT EXISTS resource_samples (
    ts        INTEGER NOT NULL,
    cpu_pct   REAL    NOT NULL,
    mem_pct   REAL    NOT NULL,
    disk_pct  REAL    NOT NULL,
    dio_read  INTEGER NOT NULL,
    dio_write INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_resource_samples_ts ON resource_samples(ts);
CREATE TABLE IF NOT EXISTS resource_hourly (
    ts_hour       INTEGER PRIMARY KEY,
    cpu_avg       REAL    NOT NULL, cpu_max       REAL    NOT NULL,
    mem_avg       REAL    NOT NULL, mem_max       REAL    NOT NULL,
    disk_avg      REAL    NOT NULL, disk_max      REAL    NOT NULL,
    dio_read_avg  INTEGER NOT NULL, dio_read_max  INTEGER NOT NULL,
    dio_write_avg INTEGER NOT NULL, dio_write_max INTEGER NOT NULL,
    sample_count  INTEGER NOT NULL DEFAULT 1
);
-- Per-address traffic mirrors the whole-node traffic above exactly: raw
-- samples at the sampling interval, folded into hourly buckets by the same
-- maintenance pass and pruned by the same cutoff. A window total is therefore
-- the same sum of the two tables that totalsSince computes for the node.
CREATE TABLE IF NOT EXISTS ip_samples (
    ts        INTEGER NOT NULL,
    ip        TEXT    NOT NULL,
    in_bytes  INTEGER NOT NULL,
    out_bytes INTEGER NOT NULL,
    PRIMARY KEY (ts, ip)
);
CREATE INDEX IF NOT EXISTS idx_ip_samples_ip ON ip_samples(ip);
CREATE TABLE IF NOT EXISTS ip_hourly (
    ts_hour   INTEGER NOT NULL,
    ip        TEXT    NOT NULL,
    in_bytes  INTEGER NOT NULL,
    out_bytes INTEGER NOT NULL,
    PRIMARY KEY (ts_hour, ip)
);
CREATE INDEX IF NOT EXISTS idx_ip_hourly_ip ON ip_hourly(ip);
-- ip_daily held day totals only. Its content is a strict subset of what the
-- pair above records, and per-address history is cleared every quota cycle
-- anyway, so it is dropped rather than migrated.
DROP TABLE IF EXISTS ip_daily;
CREATE TABLE IF NOT EXISTS ping_samples (
    ts       INTEGER NOT NULL,
    target   TEXT    NOT NULL,
    avg_ms   REAL,
    loss_pct REAL    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ping_samples_ts ON ping_samples(ts);
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	return s.ensureResourceHourlySampleCount()
}

// ensureResourceHourlySampleCount migrates databases created before the
// sample_count column existed. Legacy rows get weight 1.
func (s *Store) ensureResourceHourlySampleCount() error {
	var count int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('resource_hourly') WHERE name = 'sample_count'`,
	).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := s.db.Exec(`ALTER TABLE resource_hourly ADD COLUMN sample_count INTEGER NOT NULL DEFAULT 1`)
	return err
}

// quotaStopKey marks that this monitor stopped sing-box to enforce the quota.
const quotaStopKey = "stopped_by_quota"

// SetQuotaStopped persists whether sing-box is currently stopped by quota
// enforcement, so the state survives monitor restarts.
func (s *Store) SetQuotaStopped(stopped bool) error {
	if !stopped {
		_, err := s.db.Exec(`DELETE FROM meta WHERE key = ?`, quotaStopKey)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES(?, '1') ON CONFLICT(key) DO UPDATE SET value = '1'`, quotaStopKey)
	return err
}

// QuotaStopped reports whether a previous run stopped sing-box for the quota.
func (s *Store) QuotaStopped() (bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, quotaStopKey).Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return value == "1", nil
}

// InsertSample records one interface sample.
func (s *Store) InsertSample(ts int64, iface string, rx, tx, deltaIn, deltaOut uint64) error {
	_, err := s.db.Exec(
		`INSERT INTO samples(ts, iface, rx_bytes, tx_bytes, delta_rx_bytes, delta_tx_bytes) VALUES(?, ?, ?, ?, ?, ?)`,
		ts, iface, int64(rx), int64(tx), int64(deltaIn), int64(deltaOut),
	)
	return err
}

// TotalsSince returns in/out usage for samples and aggregated hourly buckets at
// or after since.
func (s *Store) TotalsSince(since int64) (TrafficTotals, error) {
	return totalsSince(s.db, since)
}

type rowQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func totalsSince(queryer rowQueryer, since int64) (TrafficTotals, error) {
	var inSum, outSum int64
	for _, query := range []string{
		`SELECT COALESCE(SUM(delta_rx_bytes), 0), COALESCE(SUM(delta_tx_bytes), 0) FROM samples WHERE ts >= ?`,
		`SELECT COALESCE(SUM(in_bytes), 0), COALESCE(SUM(out_bytes), 0) FROM hourly WHERE ts_hour >= ?`,
		`SELECT COALESCE(SUM(in_bytes), 0), COALESCE(SUM(out_bytes), 0) FROM adjustments WHERE ts >= ?`,
	} {
		in, out, err := sumPair(queryer, query, since)
		if err != nil {
			return TrafficTotals{}, err
		}
		inSum += in
		outSum += out
	}
	return TrafficTotals{
		InBytes:  nonNegativeUint64(inSum),
		OutBytes: nonNegativeUint64(outSum),
	}, nil
}

// sumPair runs a two-column SUM query with a single bind value and returns
// both sums.
func sumPair(queryer rowQueryer, query string, since int64) (int64, int64, error) {
	var a, b sql.NullInt64
	if err := queryer.QueryRow(query, since).Scan(&a, &b); err != nil {
		return 0, 0, err
	}
	return a.Int64, b.Int64, nil
}

// SetTotalsSince adjusts the current cycle so totals since the boundary match
// target values. It records a signed adjustment row rather than rewriting raw
// counter samples, preserving the sampled history.
func (s *Store) SetTotalsSince(since, ts int64, target TrafficTotals) error {
	_, err := s.ReplaceTotalsSince(since, ts, target)
	return err
}

// ReplaceTotalsSince is SetTotalsSince with the exact pre-commit totals
// returned to the caller. The read and adjustment insert share one database
// transaction, so a sampler using this Store cannot interleave between them.
func (s *Store) ReplaceTotalsSince(since, ts int64, target TrafficTotals) (TrafficTotals, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return TrafficTotals{}, err
	}
	defer tx.Rollback()
	current, err := totalsSince(tx, since)
	if err != nil {
		return TrafficTotals{}, err
	}
	deltaIn, err := signedDifference(target.InBytes, current.InBytes)
	if err != nil {
		return TrafficTotals{}, err
	}
	deltaOut, err := signedDifference(target.OutBytes, current.OutBytes)
	if err != nil {
		return TrafficTotals{}, err
	}
	if _, err = tx.Exec(
		`INSERT INTO adjustments(ts, in_bytes, out_bytes) VALUES(?, ?, ?)`,
		ts, deltaIn, deltaOut,
	); err != nil {
		return TrafficTotals{}, err
	}
	if err := tx.Commit(); err != nil {
		return TrafficTotals{}, err
	}
	return current, nil
}

// TrendHourly returns hourly buckets at or after since, oldest first. It unions
// already-aggregated hourly rows with on-the-fly buckets from raw samples.
func (s *Store) TrendHourly(since int64) ([]HourlyPoint, error) {
	rows, err := s.db.Query(`
SELECT ts_hour, SUM(in_bytes), SUM(out_bytes) FROM (
    SELECT ts_hour, in_bytes, out_bytes FROM hourly WHERE ts_hour >= ?1
    UNION ALL
    SELECT (ts/3600)*3600 AS ts_hour, SUM(delta_rx_bytes) AS in_bytes, SUM(delta_tx_bytes) AS out_bytes
    FROM samples WHERE ts >= ?1 GROUP BY (ts/3600)
)
GROUP BY ts_hour
ORDER BY ts_hour ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []HourlyPoint
	for rows.Next() {
		var p HourlyPoint
		if err := rows.Scan(&p.HourTS, &p.InBytes, &p.OutBytes); err != nil {
			return nil, err
		}
		p.TotalBytes = p.InBytes + p.OutBytes
		points = append(points, p)
	}
	return points, rows.Err()
}

// AggregateHourly folds raw samples older than before into the hourly table and
// deletes those raw samples. Keeping raw data bounded controls database size.
func (s *Store) AggregateHourly(before int64) error {
	return s.foldAndPrune(before, `
INSERT INTO hourly(ts_hour, in_bytes, out_bytes)
SELECT (ts/3600)*3600 AS h, SUM(delta_rx_bytes), SUM(delta_tx_bytes) FROM samples WHERE ts < ? GROUP BY h
ON CONFLICT(ts_hour) DO UPDATE SET in_bytes = in_bytes + excluded.in_bytes, out_bytes = out_bytes + excluded.out_bytes`,
		`DELETE FROM samples WHERE ts < ?`)
}

// foldAndPrune runs the fold INSERT and the raw-table DELETE in a single
// transaction so a crash never drops samples that were not yet folded.
func (s *Store) foldAndPrune(before int64, insertSQL, deleteSQL string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(insertSQL, before); err != nil {
		return err
	}
	if _, err := tx.Exec(deleteSQL, before); err != nil {
		return err
	}
	return tx.Commit()
}

// Cleanup removes hourly buckets older than the retention cutoff.
func (s *Store) Cleanup(retentionCutoff int64) error {
	if _, err := s.db.Exec(`DELETE FROM hourly WHERE ts_hour < ?`, retentionCutoff); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM adjustments WHERE ts < ?`, retentionCutoff); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM resource_hourly WHERE ts_hour < ?`, retentionCutoff); err != nil {
		return err
	}
	return nil
}

// LatestSampleTime returns the unix timestamp of the most recent traffic sample.
func (s *Store) LatestSampleTime() (int64, bool) {
	var ts int64
	switch s.db.QueryRow(`SELECT MAX(ts) FROM samples`).Scan(&ts) {
	case nil:
		return ts, ts > 0
	default:
		return 0, false
	}
}

// LatestCounters returns the most recent stored cumulative counters for iface,
// used to compute the next delta after a restart. ok is false when none exist.
func (s *Store) LatestCounters(iface string) (rx, tx uint64, ok bool, err error) {
	var r, t int64
	row := s.db.QueryRow(`SELECT rx_bytes, tx_bytes FROM samples WHERE iface = ? ORDER BY ts DESC LIMIT 1`, iface)
	switch scanErr := row.Scan(&r, &t); scanErr {
	case nil:
		return uint64(r), uint64(t), true, nil
	case sql.ErrNoRows:
		return 0, 0, false, nil
	default:
		return 0, 0, false, fmt.Errorf("latest counters: %w", scanErr)
	}
}

// InsertResourceSample records one resource reading.
func (s *Store) InsertResourceSample(ts int64, cpu, mem, disk float64, dioRead, dioWrite uint64) error {
	_, err := s.db.Exec(
		`INSERT INTO resource_samples(ts, cpu_pct, mem_pct, disk_pct, dio_read, dio_write) VALUES(?, ?, ?, ?, ?, ?)`,
		ts, cpu, mem, disk, int64(dioRead), int64(dioWrite),
	)
	return err
}

// ResourceTrendHourly returns hourly resource buckets at or after since,
// oldest first. It unions pre-aggregated hourly rows with on-the-fly
// aggregation from raw resource samples.
func (s *Store) ResourceTrendHourly(since int64) ([]ResourceHourlyPoint, error) {
	// A recent hour can exist both as a partially folded hourly row and as raw
	// samples; merge the two weighted by sample count, mirroring the fold.
	rows, err := s.db.Query(`
SELECT ts_hour,
    SUM(cpu_avg * n) / SUM(n), MAX(cpu_max), SUM(mem_avg * n) / SUM(n), MAX(mem_max),
    SUM(disk_avg * n) / SUM(n), MAX(disk_max),
    CAST(SUM(dio_read_avg * n) / SUM(n) AS INTEGER), MAX(dio_read_max),
    CAST(SUM(dio_write_avg * n) / SUM(n) AS INTEGER), MAX(dio_write_max)
FROM (
    SELECT ts_hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_avg, disk_max,
           dio_read_avg, dio_read_max, dio_write_avg, dio_write_max,
           sample_count AS n
    FROM resource_hourly WHERE ts_hour >= ?1
    UNION ALL
    SELECT (ts/3600)*3600 AS ts_hour,
           AVG(cpu_pct), MAX(cpu_pct), AVG(mem_pct), MAX(mem_pct),
           AVG(disk_pct), MAX(disk_pct),
           CAST(AVG(dio_read) AS INTEGER), MAX(dio_read),
           CAST(AVG(dio_write) AS INTEGER), MAX(dio_write),
           COUNT(*) AS n
    FROM resource_samples WHERE ts >= ?1 GROUP BY (ts/3600)
)
GROUP BY ts_hour
ORDER BY ts_hour ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []ResourceHourlyPoint
	for rows.Next() {
		var p ResourceHourlyPoint
		if err := rows.Scan(
			&p.HourTS,
			&p.CPUAvg, &p.CPUMax, &p.MemAvg, &p.MemMax,
			&p.DiskAvg, &p.DiskMax,
			&p.DIOReadAvg, &p.DIOReadMax,
			&p.DIOWriteAvg, &p.DIOWriteMax,
		); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// ResourceRawSamples returns raw resource readings at or after since.
func (s *Store) ResourceRawSamples(since int64) ([]ResourceRawPoint, error) {
	rows, err := s.db.Query(`
SELECT ts, cpu_pct, mem_pct, disk_pct, dio_read, dio_write
FROM resource_samples WHERE ts >= ? ORDER BY ts ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []ResourceRawPoint
	for rows.Next() {
		var p ResourceRawPoint
		if err := rows.Scan(&p.TS, &p.CPUPct, &p.MemPct, &p.DiskPct, &p.DIORead, &p.DIOWrite); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// TrafficRawSamples returns raw traffic deltas at or after since.
func (s *Store) TrafficRawSamples(since int64) ([]TrafficRawPoint, error) {
	rows, err := s.db.Query(`
SELECT ts, delta_rx_bytes, delta_tx_bytes
FROM samples WHERE ts >= ? ORDER BY ts ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []TrafficRawPoint
	for rows.Next() {
		var p TrafficRawPoint
		if err := rows.Scan(&p.TS, &p.InBytes, &p.OutBytes); err != nil {
			return nil, err
		}
		p.TotalBytes = p.InBytes + p.OutBytes
		points = append(points, p)
	}
	return points, rows.Err()
}

// AggregateResourceHourly folds raw resource samples older than before into
// the resource_hourly table and deletes those raw samples.
func (s *Store) AggregateResourceHourly(before int64) error {
	// Successive folds of the same hour cover unequal spans (the maintenance
	// tick is rarely hour-aligned), so averages merge weighted by sample count.
	return s.foldAndPrune(before, `
INSERT INTO resource_hourly(ts_hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_avg, disk_max,
    dio_read_avg, dio_read_max, dio_write_avg, dio_write_max, sample_count)
SELECT (ts/3600)*3600 AS h,
    AVG(cpu_pct), MAX(cpu_pct), AVG(mem_pct), MAX(mem_pct),
    AVG(disk_pct), MAX(disk_pct),
    CAST(AVG(dio_read) AS INTEGER), MAX(dio_read),
    CAST(AVG(dio_write) AS INTEGER), MAX(dio_write),
    COUNT(*)
FROM resource_samples WHERE ts < ? GROUP BY h
ON CONFLICT(ts_hour) DO UPDATE SET
    cpu_avg  = (resource_hourly.cpu_avg * resource_hourly.sample_count + excluded.cpu_avg * excluded.sample_count)
               / (resource_hourly.sample_count + excluded.sample_count),
    cpu_max  = MAX(resource_hourly.cpu_max, excluded.cpu_max),
    mem_avg  = (resource_hourly.mem_avg * resource_hourly.sample_count + excluded.mem_avg * excluded.sample_count)
               / (resource_hourly.sample_count + excluded.sample_count),
    mem_max  = MAX(resource_hourly.mem_max, excluded.mem_max),
    disk_avg = (resource_hourly.disk_avg * resource_hourly.sample_count + excluded.disk_avg * excluded.sample_count)
               / (resource_hourly.sample_count + excluded.sample_count),
    disk_max = MAX(resource_hourly.disk_max, excluded.disk_max),
    dio_read_avg  = (resource_hourly.dio_read_avg * resource_hourly.sample_count + excluded.dio_read_avg * excluded.sample_count)
                    / (resource_hourly.sample_count + excluded.sample_count),
    dio_read_max  = MAX(resource_hourly.dio_read_max, excluded.dio_read_max),
    dio_write_avg = (resource_hourly.dio_write_avg * resource_hourly.sample_count + excluded.dio_write_avg * excluded.sample_count)
                    / (resource_hourly.sample_count + excluded.sample_count),
    dio_write_max = MAX(resource_hourly.dio_write_max, excluded.dio_write_max),
    sample_count  = resource_hourly.sample_count + excluded.sample_count`,
		`DELETE FROM resource_samples WHERE ts < ?`)
}

// ipTrafficCycleKey records the quota cycle the per-IP table describes.
const ipTrafficCycleKey = "ip_traffic_cycle_start"

// AddIPTraffic records one round of per-address deltas as raw samples, exactly
// as InsertSample records the node's own counters.
func (s *Store) AddIPTraffic(ts int64, deltas map[string]IPTrafficDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
INSERT INTO ip_samples(ts, ip, in_bytes, out_bytes) VALUES(?, ?, ?, ?)
ON CONFLICT(ts, ip) DO UPDATE SET
    in_bytes = in_bytes + excluded.in_bytes,
    out_bytes = out_bytes + excluded.out_bytes`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for address, delta := range deltas {
		if _, err := stmt.Exec(ts, address, int64(delta.InBytes), int64(delta.OutBytes)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AggregateIPHourly folds raw per-address samples older than before into hourly
// buckets, the same fold AggregateHourly performs for the node as a whole and
// in the same maintenance pass, so both histories thin out together.
func (s *Store) AggregateIPHourly(before int64) error {
	return s.foldAndPrune(before, `
INSERT INTO ip_hourly(ts_hour, ip, in_bytes, out_bytes)
SELECT (ts/3600)*3600 AS h, ip, SUM(in_bytes), SUM(out_bytes) FROM ip_samples WHERE ts < ? GROUP BY h, ip
ON CONFLICT(ts_hour, ip) DO UPDATE SET in_bytes = in_bytes + excluded.in_bytes, out_bytes = out_bytes + excluded.out_bytes`,
		`DELETE FROM ip_samples WHERE ts < ?`)
}

// ResetIPTrafficForCycle clears the per-address history when the quota cycle
// rolls over, so the top list always describes the cycle the quota is counting.
// Bucket timestamps cannot express a reset hour on their own, so the boundary
// is tracked explicitly. It reports whether it cleared anything.
func (s *Store) ResetIPTrafficForCycle(cycleStart int64) (bool, error) {
	var recorded string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, ipTrafficCycleKey).Scan(&recorded)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	want := strconv.FormatInt(cycleStart, 10)
	if err == nil && recorded == want {
		return false, nil
	}
	tx, txErr := s.db.Begin()
	if txErr != nil {
		return false, txErr
	}
	defer tx.Rollback()
	// A first run has nothing to clear but still has to record the boundary, or
	// the next sample would look like a rollover.
	cleared := err == nil
	if cleared {
		for _, table := range []string{"ip_samples", "ip_hourly"} {
			if _, execErr := tx.Exec(`DELETE FROM ` + table); execErr != nil {
				return false, execErr
			}
		}
	}
	if _, execErr := tx.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		ipTrafficCycleKey, want,
	); execErr != nil {
		return false, execErr
	}
	return cleared, tx.Commit()
}

// PruneIPTraffic keeps only the busiest addresses. A node facing mobile clients
// sees a long tail of one-off addresses that can never reach the top list, and
// dropping them is what keeps the tables small on a 256 MB VPS. Ranking spans
// both tables so an address is judged on its whole cycle, not on whichever part
// of it has been folded.
func (s *Store) PruneIPTraffic(keep int) error {
	if keep <= 0 {
		return nil
	}
	const survivors = `
SELECT ip FROM (
    SELECT ip, SUM(bytes) AS bytes FROM (
        SELECT ip, SUM(in_bytes + out_bytes) AS bytes FROM ip_samples GROUP BY ip
        UNION ALL
        SELECT ip, SUM(in_bytes + out_bytes) AS bytes FROM ip_hourly GROUP BY ip
    ) GROUP BY ip
    ORDER BY bytes DESC
    LIMIT ?
)`
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"ip_samples", "ip_hourly"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE ip NOT IN (`+survivors+`)`, keep); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TopIPTraffic ranks addresses by their traffic since cycleStart and reports
// each one's totals over the three windows the dashboard sorts by. Every window
// sums the raw table and the hourly table, which is the same reading
// totalsSince performs for the node itself; the boundaries are all hour-aligned
// so no bucket is split between windows.
func (s *Store) TopIPTraffic(limit int, cycleStart, todayStart, weekStart int64) ([]IPTrafficEntry, error) {
	rows, err := s.db.Query(`
SELECT ip,
       SUM(cycle_in),  SUM(cycle_out),
       SUM(today_in),  SUM(today_out),
       SUM(week_in),   SUM(week_out)
FROM (
    SELECT ip,
           SUM(CASE WHEN ts >= ?1 THEN in_bytes  ELSE 0 END) AS cycle_in,
           SUM(CASE WHEN ts >= ?1 THEN out_bytes ELSE 0 END) AS cycle_out,
           SUM(CASE WHEN ts >= ?2 THEN in_bytes  ELSE 0 END) AS today_in,
           SUM(CASE WHEN ts >= ?2 THEN out_bytes ELSE 0 END) AS today_out,
           SUM(CASE WHEN ts >= ?3 THEN in_bytes  ELSE 0 END) AS week_in,
           SUM(CASE WHEN ts >= ?3 THEN out_bytes ELSE 0 END) AS week_out
    FROM ip_samples GROUP BY ip
    UNION ALL
    SELECT ip,
           SUM(CASE WHEN ts_hour >= ?1 THEN in_bytes  ELSE 0 END),
           SUM(CASE WHEN ts_hour >= ?1 THEN out_bytes ELSE 0 END),
           SUM(CASE WHEN ts_hour >= ?2 THEN in_bytes  ELSE 0 END),
           SUM(CASE WHEN ts_hour >= ?2 THEN out_bytes ELSE 0 END),
           SUM(CASE WHEN ts_hour >= ?3 THEN in_bytes  ELSE 0 END),
           SUM(CASE WHEN ts_hour >= ?3 THEN out_bytes ELSE 0 END)
    FROM ip_hourly GROUP BY ip
)
GROUP BY ip
HAVING SUM(cycle_in) + SUM(cycle_out) > 0
ORDER BY SUM(cycle_in) + SUM(cycle_out) DESC
LIMIT ?4`, cycleStart, todayStart, weekStart, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []IPTrafficEntry
	for rows.Next() {
		var e IPTrafficEntry
		if err := rows.Scan(&e.IP,
			&e.Cycle.InBytes, &e.Cycle.OutBytes,
			&e.Today.InBytes, &e.Today.OutBytes,
			&e.Last7.InBytes, &e.Last7.OutBytes,
		); err != nil {
			return nil, err
		}
		e.Cycle.total()
		e.Today.total()
		e.Last7.total()
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// IPTrafficSeries returns one address's history at the three granularities the
// dashboard draws, each read the same way the node's own series is: raw rows
// for the recent window, hourly buckets for the hourly view, and those buckets
// folded into GMT days for the daily view.
func (s *Store) IPTrafficSeries(address string, recentSince, since int64) (IPTrafficDetail, error) {
	detail := IPTrafficDetail{IP: address}
	recent, err := s.ipPoints(`SELECT ts, in_bytes, out_bytes FROM ip_samples WHERE ip = ? AND ts >= ? ORDER BY ts ASC`, address, recentSince)
	if err != nil {
		return IPTrafficDetail{}, err
	}
	detail.Recent = recent
	// The hourly view has to include the samples not folded yet, or its most
	// recent hours read as empty until maintenance catches up.
	hourly, err := s.ipBuckets(address, since, 3600)
	if err != nil {
		return IPTrafficDetail{}, err
	}
	detail.Hourly = hourly
	daily, err := s.ipBuckets(address, since, 86400)
	if err != nil {
		return IPTrafficDetail{}, err
	}
	detail.Daily = daily
	return detail, nil
}

func (s *Store) ipBuckets(address string, since, width int64) ([]IPSeriesPoint, error) {
	return s.ipPoints(`
SELECT bucket, SUM(in_bytes), SUM(out_bytes) FROM (
    SELECT (ts/?2)*?2 AS bucket, in_bytes, out_bytes FROM ip_samples WHERE ip = ?1 AND ts >= ?3
    UNION ALL
    SELECT (ts_hour/?2)*?2 AS bucket, in_bytes, out_bytes FROM ip_hourly WHERE ip = ?1 AND ts_hour >= ?3
)
GROUP BY bucket ORDER BY bucket ASC`, address, width, since)
}

func (s *Store) ipPoints(query string, args ...any) ([]IPSeriesPoint, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := []IPSeriesPoint{}
	for rows.Next() {
		var p IPSeriesPoint
		if err := rows.Scan(&p.TS, &p.InBytes, &p.OutBytes); err != nil {
			return nil, err
		}
		p.TotalBytes = p.InBytes + p.OutBytes
		points = append(points, p)
	}
	return points, rows.Err()
}

// InsertPingSamples records one latency round, keyed by target ID.
func (s *Store) InsertPingSamples(ts int64, samples map[string]PingSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO ping_samples(ts, target, avg_ms, loss_pct) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for target, sample := range samples {
		var avg any
		if sample.AvgMS != nil {
			avg = *sample.AvgMS
		}
		if _, err := stmt.Exec(ts, target, avg, sample.LossPct); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PingTrendHourly returns per-target hourly averages at or after since, oldest
// first. Averaging at read time keeps a week of one-minute samples small
// enough to ship in one response. AVG skips the NULL latencies of fully lost
// rounds, so the average describes the connects that did come back.
func (s *Store) PingTrendHourly(since int64) ([]PingHourlyPoint, error) {
	return s.pingBuckets(since, 3600)
}

// PingTrendDaily folds the same samples into GMT days, which is the window the
// dashboard's daily view draws.
func (s *Store) PingTrendDaily(since int64) ([]PingHourlyPoint, error) {
	return s.pingBuckets(since, 86400)
}

func (s *Store) pingBuckets(since, width int64) ([]PingHourlyPoint, error) {
	rows, err := s.db.Query(`
SELECT (ts/?)*? AS bucket, target, AVG(avg_ms), AVG(loss_pct)
FROM ping_samples WHERE ts >= ?
GROUP BY bucket, target
ORDER BY bucket ASC`, width, width, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []PingHourlyPoint
	for rows.Next() {
		var (
			p   PingHourlyPoint
			avg sql.NullFloat64
		)
		if err := rows.Scan(&p.HourTS, &p.Target, &avg, &p.LossPct); err != nil {
			return nil, err
		}
		if avg.Valid {
			value := avg.Float64
			p.AvgMS = &value
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// PingRawSamples returns every sample at or after since, oldest first. It backs
// the dashboard's per-minute "recent" view, so the window it is called with is
// deliberately short.
func (s *Store) PingRawSamples(since int64) ([]PingRawPoint, error) {
	rows, err := s.db.Query(`
SELECT ts, target, avg_ms, loss_pct FROM ping_samples
WHERE ts >= ? ORDER BY ts ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []PingRawPoint
	for rows.Next() {
		var (
			p   PingRawPoint
			avg sql.NullFloat64
		)
		if err := rows.Scan(&p.TS, &p.Target, &avg, &p.LossPct); err != nil {
			return nil, err
		}
		if avg.Valid {
			value := avg.Float64
			p.AvgMS = &value
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// LatestPingSamples returns the most recent sample per target at or after
// since. The bare columns pair with MAX(ts) under SQLite's documented rule that
// they are taken from the row the aggregate selected.
func (s *Store) LatestPingSamples(since int64) ([]PingLatestPoint, error) {
	rows, err := s.db.Query(`
SELECT target, MAX(ts), avg_ms, loss_pct
FROM ping_samples WHERE ts >= ?
GROUP BY target`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []PingLatestPoint
	for rows.Next() {
		var (
			p   PingLatestPoint
			avg sql.NullFloat64
		)
		if err := rows.Scan(&p.Target, &p.TS, &avg, &p.LossPct); err != nil {
			return nil, err
		}
		if avg.Valid {
			value := avg.Float64
			p.AvgMS = &value
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// CleanupPingSamples drops latency samples older than the cutoff.
func (s *Store) CleanupPingSamples(cutoff int64) error {
	_, err := s.db.Exec(`DELETE FROM ping_samples WHERE ts < ?`, cutoff)
	return err
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func nonNegativeUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func signedDifference(target, current uint64) (int64, error) {
	if target > math.MaxInt64 || current > math.MaxInt64 {
		return 0, fmt.Errorf("traffic total exceeds supported adjustment range")
	}
	return int64(target) - int64(current), nil
}
