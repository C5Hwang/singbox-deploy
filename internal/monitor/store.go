package monitor

import (
	"database/sql"
	"fmt"
	"math"

	_ "modernc.org/sqlite"
)

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
	return s, nil
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
    dio_write_avg INTEGER NOT NULL, dio_write_max INTEGER NOT NULL
);`
	_, err := s.db.Exec(schema)
	return err
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
	var rawIn, rawOut sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(delta_rx_bytes), 0), COALESCE(SUM(delta_tx_bytes), 0) FROM samples WHERE ts >= ?`, since,
	).Scan(&rawIn, &rawOut); err != nil {
		return TrafficTotals{}, err
	}
	var aggIn, aggOut sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(in_bytes), 0), COALESCE(SUM(out_bytes), 0) FROM hourly WHERE ts_hour >= ?`, since,
	).Scan(&aggIn, &aggOut); err != nil {
		return TrafficTotals{}, err
	}
	var adjIn, adjOut sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(in_bytes), 0), COALESCE(SUM(out_bytes), 0) FROM adjustments WHERE ts >= ?`, since,
	).Scan(&adjIn, &adjOut); err != nil {
		return TrafficTotals{}, err
	}
	return TrafficTotals{
		InBytes:  nonNegativeUint64(rawIn.Int64 + aggIn.Int64 + adjIn.Int64),
		OutBytes: nonNegativeUint64(rawOut.Int64 + aggOut.Int64 + adjOut.Int64),
	}, nil
}

// SetTotalsSince adjusts the current cycle so totals since the boundary match
// target values. It records a signed adjustment row rather than rewriting raw
// counter samples, preserving the sampled history.
func (s *Store) SetTotalsSince(since, ts int64, target TrafficTotals) error {
	current, err := s.TotalsSince(since)
	if err != nil {
		return err
	}
	deltaIn, err := signedDifference(target.InBytes, current.InBytes)
	if err != nil {
		return err
	}
	deltaOut, err := signedDifference(target.OutBytes, current.OutBytes)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO adjustments(ts, in_bytes, out_bytes) VALUES(?, ?, ?)`,
		ts, deltaIn, deltaOut,
	)
	return err
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
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
INSERT INTO hourly(ts_hour, in_bytes, out_bytes)
SELECT (ts/3600)*3600 AS h, SUM(delta_rx_bytes), SUM(delta_tx_bytes) FROM samples WHERE ts < ? GROUP BY h
ON CONFLICT(ts_hour) DO UPDATE SET in_bytes = in_bytes + excluded.in_bytes, out_bytes = out_bytes + excluded.out_bytes`, before); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM samples WHERE ts < ?`, before); err != nil {
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
	rows, err := s.db.Query(`
SELECT ts_hour,
    AVG(cpu_avg), MAX(cpu_max), AVG(mem_avg), MAX(mem_max),
    AVG(disk_avg), MAX(disk_max),
    CAST(AVG(dio_read_avg) AS INTEGER), MAX(dio_read_max),
    CAST(AVG(dio_write_avg) AS INTEGER), MAX(dio_write_max)
FROM (
    SELECT ts_hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_avg, disk_max,
           dio_read_avg, dio_read_max, dio_write_avg, dio_write_max
    FROM resource_hourly WHERE ts_hour >= ?1
    UNION ALL
    SELECT (ts/3600)*3600 AS ts_hour,
           AVG(cpu_pct), MAX(cpu_pct), AVG(mem_pct), MAX(mem_pct),
           AVG(disk_pct), MAX(disk_pct),
           CAST(AVG(dio_read) AS INTEGER), MAX(dio_read),
           CAST(AVG(dio_write) AS INTEGER), MAX(dio_write)
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
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
INSERT INTO resource_hourly(ts_hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_avg, disk_max,
    dio_read_avg, dio_read_max, dio_write_avg, dio_write_max)
SELECT (ts/3600)*3600 AS h,
    AVG(cpu_pct), MAX(cpu_pct), AVG(mem_pct), MAX(mem_pct),
    AVG(disk_pct), MAX(disk_pct),
    CAST(AVG(dio_read) AS INTEGER), MAX(dio_read),
    CAST(AVG(dio_write) AS INTEGER), MAX(dio_write)
FROM resource_samples WHERE ts < ? GROUP BY h
ON CONFLICT(ts_hour) DO UPDATE SET
    cpu_avg  = (resource_hourly.cpu_avg + excluded.cpu_avg) / 2,
    cpu_max  = MAX(resource_hourly.cpu_max, excluded.cpu_max),
    mem_avg  = (resource_hourly.mem_avg + excluded.mem_avg) / 2,
    mem_max  = MAX(resource_hourly.mem_max, excluded.mem_max),
    disk_avg = (resource_hourly.disk_avg + excluded.disk_avg) / 2,
    disk_max = MAX(resource_hourly.disk_max, excluded.disk_max),
    dio_read_avg  = (resource_hourly.dio_read_avg + excluded.dio_read_avg) / 2,
    dio_read_max  = MAX(resource_hourly.dio_read_max, excluded.dio_read_max),
    dio_write_avg = (resource_hourly.dio_write_avg + excluded.dio_write_avg) / 2,
    dio_write_max = MAX(resource_hourly.dio_write_max, excluded.dio_write_max)`, before); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM resource_samples WHERE ts < ?`, before); err != nil {
		return err
	}
	return tx.Commit()
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
