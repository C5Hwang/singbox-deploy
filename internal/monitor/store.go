package monitor

import (
	"database/sql"
	"fmt"

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
	HourTS int64 `json:"hourTs"`
	Bytes  int64 `json:"bytes"`
}

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
    delta_bytes INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples(ts);
CREATE TABLE IF NOT EXISTS hourly (
    ts_hour INTEGER PRIMARY KEY,
    bytes   INTEGER NOT NULL
);`
	_, err := s.db.Exec(schema)
	return err
}

// InsertSample records one interface sample.
func (s *Store) InsertSample(ts int64, iface string, rx, tx, delta uint64) error {
	_, err := s.db.Exec(
		`INSERT INTO samples(ts, iface, rx_bytes, tx_bytes, delta_bytes) VALUES(?, ?, ?, ?, ?)`,
		ts, iface, int64(rx), int64(tx), int64(delta),
	)
	return err
}

// TotalSince returns the summed delta_bytes for samples and aggregated hourly
// buckets at or after since.
func (s *Store) TotalSince(since int64) (uint64, error) {
	var raw sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(delta_bytes), 0) FROM samples WHERE ts >= ?`, since,
	).Scan(&raw); err != nil {
		return 0, err
	}
	var agg sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(bytes), 0) FROM hourly WHERE ts_hour >= ?`, since,
	).Scan(&agg); err != nil {
		return 0, err
	}
	return uint64(raw.Int64 + agg.Int64), nil
}

// TrendHourly returns hourly buckets at or after since, oldest first. It unions
// already-aggregated hourly rows with on-the-fly buckets from raw samples.
func (s *Store) TrendHourly(since int64) ([]HourlyPoint, error) {
	rows, err := s.db.Query(`
SELECT ts_hour, bytes FROM (
    SELECT ts_hour, bytes FROM hourly WHERE ts_hour >= ?1
    UNION ALL
    SELECT (ts/3600)*3600 AS ts_hour, SUM(delta_bytes) AS bytes
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
		if err := rows.Scan(&p.HourTS, &p.Bytes); err != nil {
			return nil, err
		}
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
INSERT INTO hourly(ts_hour, bytes)
SELECT (ts/3600)*3600 AS h, SUM(delta_bytes) FROM samples WHERE ts < ? GROUP BY h
ON CONFLICT(ts_hour) DO UPDATE SET bytes = bytes + excluded.bytes`, before); err != nil {
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
	return nil
}

// Vacuum reclaims free pages. Run only when convenient (it rewrites the file).
func (s *Store) Vacuum() error {
	_, err := s.db.Exec(`VACUUM`)
	return err
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

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }
