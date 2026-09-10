package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// SessionOpened records a player arriving and returns the row's id.
//
// The row is written when they arrive rather than when they leave, so a
// Garrison that is killed mid-session leaves evidence that somebody was on
// rather than losing the session entirely. The cost is rows with no left_at,
// which SessionsSince reports as open and CloseStaleSessions tidies.
func (d *DB) SessionOpened(server string, p model.Player, at time.Time) (int64, error) {
	res, err := d.sql.Exec(
		`INSERT INTO sessions (server, player, steam_id, joined_at, left_at) VALUES (?, ?, ?, ?, NULL)`,
		server, p.Name, p.SteamID, at.UTC().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("%s: recording that %s joined: %w", server, p.Name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("%s: recording that %s joined: %w", server, p.Name, err)
	}
	return id, nil
}

// SessionClosed records a player leaving.
func (d *DB) SessionClosed(id int64, at time.Time) error {
	if _, err := d.sql.Exec(
		`UPDATE sessions SET left_at = ? WHERE id = ? AND left_at IS NULL`,
		at.UTC().UnixMilli(), id); err != nil {
		return fmt.Errorf("recording the end of session %d: %w", id, err)
	}
	return nil
}

// SessionsSince reads a server's sessions, newest first.
func (d *DB) SessionsSince(server string, since time.Time) ([]model.Session, error) {
	rows, err := d.sql.Query(
		`SELECT player, steam_id, joined_at, left_at FROM sessions
		 WHERE server = ? AND joined_at >= ? ORDER BY joined_at DESC`,
		server, since.UTC().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("%s: reading sessions: %w", server, err)
	}
	defer rows.Close()

	var out []model.Session
	for rows.Next() {
		var (
			s      model.Session
			joined int64
			left   sql.NullInt64
		)
		if err := rows.Scan(&s.Player, &s.SteamID, &joined, &left); err != nil {
			return nil, fmt.Errorf("%s: reading sessions: %w", server, err)
		}
		s.Server = server
		s.Joined = time.UnixMilli(joined).UTC()
		if left.Valid {
			s.Left = time.UnixMilli(left.Int64).UTC()
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CloseStaleSessions ends sessions left open by a Garrison that stopped
// without seeing anyone leave.
//
// They are closed at the time Garrison last saw them rather than now: a
// session left open across three days of downtime is not a three-day session,
// and reporting it as one would put a fictional player at the top of every
// occupancy chart.
func (d *DB) CloseStaleSessions(at time.Time) (int64, error) {
	res, err := d.sql.Exec(`UPDATE sessions SET left_at = joined_at WHERE left_at IS NULL AND joined_at < ?`,
		at.UTC().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("closing stale sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PruneSessions drops sessions that started before a cutoff.
func (d *DB) PruneSessions(before time.Time) (int64, error) {
	res, err := d.sql.Exec(`DELETE FROM sessions WHERE joined_at < ?`, before.UTC().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("pruning sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
