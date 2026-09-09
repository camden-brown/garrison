package store

import (
	"fmt"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// Series names for the cold tier. Strings rather than an enum because they are
// written to disk and read back by a later version.
const (
	SeriesCPU     = "cpu"
	SeriesMemory  = "memory"
	SeriesNetwork = "network"
	SeriesPlayers = "players"
)

// RecordMetric stores one cold-tier point — the minute-resolution history that
// answers "was it like this last Tuesday" and survives a restart.
//
// The hot and warm tiers stay in memory: they are five minutes and an hour,
// they are read on every frame, and writing them here would be thousands of
// rows an hour to answer a question nobody asks of the disk.
func (d *DB) RecordMetric(server, series string, p model.Point) error {
	_, err := d.sql.Exec(`
		INSERT INTO metrics (server, series, at, mean, min, max)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(server, series, at) DO UPDATE SET
			mean = excluded.mean, min = excluded.min, max = excluded.max`,
		server, series, p.At.UnixMilli(), p.Mean, p.Min, p.Max)
	if err != nil {
		return fmt.Errorf("%s/%s: recording a metric: %w", server, series, err)
	}
	return nil
}

// Metrics reads a server's cold history for a series, oldest first.
func (d *DB) Metrics(server, series string, since time.Time) ([]model.Point, error) {
	rows, err := d.sql.Query(`
		SELECT at, mean, min, max FROM metrics
		WHERE server = ? AND series = ? AND at >= ?
		ORDER BY at`, server, series, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("%s/%s: reading metrics: %w", server, series, err)
	}
	defer rows.Close()

	var out []model.Point
	for rows.Next() {
		var at int64
		var p model.Point
		if err := rows.Scan(&at, &p.Mean, &p.Min, &p.Max); err != nil {
			return nil, fmt.Errorf("%s/%s: reading a metric: %w", server, series, err)
		}
		p.At = time.UnixMilli(at).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// PruneMetrics drops points older than the cutoff. DESIGN gives the cold tier
// thirty days; past that the rows answer nothing anybody asks and cost disk
// forever.
func (d *DB) PruneMetrics(before time.Time) (int64, error) {
	res, err := d.sql.Exec(`DELETE FROM metrics WHERE at < ?`, before.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("pruning metrics: %w", err)
	}
	return res.RowsAffected()
}

// RecordEvent stores one log event worth keeping.
//
// Only chat, joins, leaves, deaths and admin actions. Writing every INFO line
// here would grow gigabytes a week and answer no question anybody will ask —
// the console ring already covers "what just happened".
func (d *DB) RecordEvent(server string, ev model.Event) error {
	if !Keepable(ev.Kind) {
		return nil
	}
	_, err := d.sql.Exec(`
		INSERT INTO events (server, at, kind, player, steam_id, text)
		VALUES (?, ?, ?, ?, ?, ?)`,
		server, ev.At.UnixMilli(), ev.Kind.String(), ev.Player, ev.SteamID, ev.Text)
	if err != nil {
		return fmt.Errorf("%s: recording an event: %w", server, err)
	}
	return nil
}

// Keepable reports whether an event kind is worth keeping for ninety days.
func Keepable(k model.Kind) bool {
	switch k {
	case model.KindJoin, model.KindLeave, model.KindDeath, model.KindChat, model.KindAdmin:
		return true
	}
	return false
}

// Events reads a server's kept history, newest first.
func (d *DB) Events(server string, since time.Time, limit int) ([]model.Event, error) {
	rows, err := d.sql.Query(`
		SELECT at, kind, player, steam_id, text FROM events
		WHERE server = ? AND at >= ?
		ORDER BY at DESC LIMIT ?`, server, since.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("%s: reading events: %w", server, err)
	}
	defer rows.Close()

	var out []model.Event
	for rows.Next() {
		var at int64
		var kind string
		var ev model.Event
		if err := rows.Scan(&at, &kind, &ev.Player, &ev.SteamID, &ev.Text); err != nil {
			return nil, fmt.Errorf("%s: reading an event: %w", server, err)
		}
		ev.At = time.UnixMilli(at).UTC()
		ev.Kind = kindFrom(kind)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// PruneEvents drops events older than the cutoff. DESIGN keeps them ninety
// days.
func (d *DB) PruneEvents(before time.Time) (int64, error) {
	res, err := d.sql.Exec(`DELETE FROM events WHERE at < ?`, before.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("pruning events: %w", err)
	}
	return res.RowsAffected()
}

func kindFrom(s string) model.Kind {
	for k := model.KindUnknown; k <= model.KindConnect; k++ {
		if k.String() == s {
			return k
		}
	}
	return model.KindUnknown
}
