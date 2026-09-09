package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/camden-brown/garrison/internal/tasks"
)

// Journal is the durable task record. It satisfies tasks.Journal.
type Journal struct{ db *DB }

// Journal returns the task journal.
func (d *DB) Journal() *Journal { return &Journal{db: d} }

var _ tasks.Journal = (*Journal)(nil)

// Record writes a task's progress, replacing what was there.
//
// The engine calls this before every step, so it is on the hot path of every
// task — but a task has single-digit steps and they take seconds each, so one
// small upsert per step costs nothing measurable and buys surviving a crash.
func (j *Journal) Record(p tasks.Progress) error {
	steps, err := json.Marshal(p.Steps)
	if err != nil {
		return fmt.Errorf("%s: encoding steps: %w", p.ID, err)
	}
	history, err := json.Marshal(p.History)
	if err != nil {
		return fmt.Errorf("%s: encoding history: %w", p.ID, err)
	}

	_, err = j.db.sql.Exec(`
		INSERT INTO tasks (id, server, kind, trigger, state, cursor, steps, err, history,
		                   queued_at, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			state = excluded.state, cursor = excluded.cursor, err = excluded.err,
			history = excluded.history, started_at = excluded.started_at,
			ended_at = excluded.ended_at`,
		p.ID, p.Server, string(p.Kind), p.Trigger.String(), p.State.String(), p.Cursor,
		string(steps), p.Err, string(history),
		nullTime(p.Queued), nullTime(p.Started), nullTime(p.Ended),
	)
	if err != nil {
		return fmt.Errorf("%s: recording progress: %w", p.ID, err)
	}
	return nil
}

// Interrupted returns tasks a previous process left running.
//
// It reads them rather than repairing them: deciding what an interrupted task
// means is the engine's job, and it marks them failed rather than resuming —
// automatically continuing a half-finished volume operation is how a world
// gets lost.
func (j *Journal) Interrupted() ([]tasks.Progress, error) {
	rows, err := j.db.sql.Query(`
		SELECT id, server, kind, trigger, cursor, steps, err, history, queued_at, started_at
		FROM tasks WHERE state IN ('running', 'queued', 'paused')`)
	if err != nil {
		return nil, fmt.Errorf("reading interrupted tasks: %w", err)
	}
	defer rows.Close()

	var out []tasks.Progress
	for rows.Next() {
		var (
			p               tasks.Progress
			kind, trigger   string
			steps, history  string
			queued, started sql.NullInt64
		)
		if err := rows.Scan(&p.ID, &p.Server, &kind, &trigger, &p.Cursor,
			&steps, &p.Err, &history, &queued, &started); err != nil {
			return nil, fmt.Errorf("reading an interrupted task: %w", err)
		}

		p.Kind = tasks.Kind(kind)
		p.Trigger = triggerFrom(trigger)
		p.State = tasks.StateRunning
		_ = json.Unmarshal([]byte(steps), &p.Steps)
		_ = json.Unmarshal([]byte(history), &p.History)
		p.Queued, p.Started = timeFrom(queued), timeFrom(started)

		out = append(out, p)
	}
	return out, rows.Err()
}

// Recent returns the newest tasks, for the history a Tasks view shows beyond
// what is still in memory.
func (j *Journal) Recent(server string, limit int) ([]tasks.Progress, error) {
	query := `SELECT id, server, kind, trigger, state, cursor, steps, err, history,
	                 queued_at, started_at, ended_at
	          FROM tasks`
	args := []any{}
	if server != "" {
		query += ` WHERE server = ?`
		args = append(args, server)
	}
	query += ` ORDER BY COALESCE(started_at, queued_at) DESC LIMIT ?`
	args = append(args, limit)

	rows, err := j.db.sql.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading task history: %w", err)
	}
	defer rows.Close()

	var out []tasks.Progress
	for rows.Next() {
		var (
			p                      tasks.Progress
			kind, trigger, state   string
			steps, history         string
			queued, started, ended sql.NullInt64
		)
		if err := rows.Scan(&p.ID, &p.Server, &kind, &trigger, &state, &p.Cursor,
			&steps, &p.Err, &history, &queued, &started, &ended); err != nil {
			return nil, fmt.Errorf("reading a task: %w", err)
		}

		p.Kind = tasks.Kind(kind)
		p.Trigger = triggerFrom(trigger)
		p.State = stateFrom(state)
		_ = json.Unmarshal([]byte(steps), &p.Steps)
		_ = json.Unmarshal([]byte(history), &p.History)
		p.Queued, p.Started, p.Ended = timeFrom(queued), timeFrom(started), timeFrom(ended)

		out = append(out, p)
	}
	return out, rows.Err()
}

// Prune drops finished tasks older than the cutoff. Everything here is bounded
// on purpose; a journal that only grows is a slow leak with a paper trail.
func (j *Journal) Prune(before time.Time) (int64, error) {
	res, err := j.db.sql.Exec(
		`DELETE FROM tasks WHERE ended_at IS NOT NULL AND ended_at < ?`, before.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("pruning tasks: %w", err)
	}
	return res.RowsAffected()
}

// States and triggers are stored as words rather than numbers, so a person
// reading the database with the sqlite3 command can tell what happened, and so
// renumbering an enum in Go cannot silently reinterpret old rows.
func stateFrom(s string) tasks.State {
	for state := tasks.StateQueued; state <= tasks.StateRolledBack; state++ {
		if state.String() == s {
			return state
		}
	}
	return tasks.StateFailed
}

func triggerFrom(s string) tasks.Trigger {
	for t := tasks.TriggerManual; t <= tasks.TriggerChained; t++ {
		if t.String() == s {
			return t
		}
	}
	return tasks.TriggerManual
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func timeFrom(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
}
