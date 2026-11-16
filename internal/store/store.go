// Package store is the SQLite layer: schema, tasks and the key/value state table.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registers "sqlite"
)

const (
	StatusOpen    = "open"
	StatusDone    = "done"
	StatusRemoved = "removed"
)

// State keys (see AGENTS.md).
const (
	KeyLastInboundAt         = "last_inbound_at"
	KeyLastMorningSent       = "last_morning_sent"
	KeyLastNightSent         = "last_night_sent"
	KeyLastRecapSent         = "last_recap_sent"
	KeyAwaitingClarification = "awaiting_clarification"
	KeyDisplayMap            = "display_map"
)

var ErrNotFound = errors.New("not found")

type Task struct {
	ID          int64
	Text        string
	Date        string // YYYY-MM-DD, owner's timezone
	Status      string
	CreatedAt   string // RFC3339 UTC
	CompletedAt string // RFC3339 UTC, empty if not done
}

// querier is satisfied by both *sql.DB and *sql.Tx, so every helper works in a transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Store struct {
	db  *sql.DB
	q   querier
	now func() time.Time
}

// migrations[i] upgrades the schema from version i to i+1. Append only.
var migrations = []string{
	`CREATE TABLE tasks (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		text         TEXT NOT NULL,
		date         TEXT NOT NULL,
		status       TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','done','removed')),
		created_at   TEXT NOT NULL,
		completed_at TEXT
	);
	CREATE INDEX idx_tasks_status_date ON tasks(status, date);
	CREATE TABLE state (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
}

// Open opens (creating if needed) the SQLite file at path and migrates it.
// now supplies timestamps; pass nil for time.Now.
func Open(path string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1) // one writer; avoids SQLITE_BUSY between goroutines
	s := &Store{db: db, q: db, now: now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	ctx := context.Background()
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for v := version; v < len(migrations); v++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("set schema version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// WithTx runs fn in a transaction. fn gets a Store bound to the tx; any error rolls back.
func (s *Store) WithTx(fn func(tx *Store) error) error {
	if _, nested := s.q.(*sql.Tx); nested {
		return fn(s)
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(&Store{db: s.db, q: tx, now: s.now}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// ---- tasks ----

func (s *Store) AddTask(text, date string) (Task, error) {
	t := Task{Text: text, Date: date, Status: StatusOpen, CreatedAt: s.stamp()}
	res, err := s.q.ExecContext(context.Background(),
		`INSERT INTO tasks (text, date, status, created_at) VALUES (?, ?, ?, ?)`,
		t.Text, t.Date, t.Status, t.CreatedAt)
	if err != nil {
		return Task{}, fmt.Errorf("add task: %w", err)
	}
	t.ID, err = res.LastInsertId()
	return t, err
}

func (s *Store) GetTask(id int64) (Task, error) {
	tasks, err := s.queryTasks(`WHERE id = ?`, id)
	if err != nil {
		return Task{}, err
	}
	if len(tasks) == 0 {
		return Task{}, ErrNotFound
	}
	return tasks[0], nil
}

// OpenOn returns open tasks planned for date, in insertion order.
func (s *Store) OpenOn(date string) ([]Task, error) {
	return s.queryTasks(`WHERE status = 'open' AND date = ? ORDER BY id`, date)
}

// OpenBefore returns open tasks planned before date (carry-over candidates), oldest first.
func (s *Store) OpenBefore(date string) ([]Task, error) {
	return s.queryTasks(`WHERE status = 'open' AND date < ? ORDER BY date, id`, date)
}

// AllOpen returns every open task, by date then insertion order.
func (s *Store) AllOpen() ([]Task, error) {
	return s.queryTasks(`WHERE status = 'open' ORDER BY date, id`)
}

// ExistsOn reports whether a non-removed task with exactly this text exists on date
// (used to dedupe recurring tasks).
func (s *Store) ExistsOn(text, date string) (bool, error) {
	var n int
	err := s.q.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM tasks WHERE text = ? AND date = ? AND status != 'removed'`, text, date).Scan(&n)
	return n > 0, err
}

// DoneBetween returns tasks completed in [from, to) (UTC instants), oldest first.
func (s *Store) DoneBetween(from, to time.Time) ([]Task, error) {
	return s.queryTasks(`WHERE status = 'done' AND completed_at >= ? AND completed_at < ? ORDER BY completed_at`,
		from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
}

// SetStatus marks a task open/done/removed. completed_at is set for done, cleared otherwise.
func (s *Store) SetStatus(id int64, status string) error {
	var completed any
	if status == StatusDone {
		completed = s.stamp()
	}
	return s.execOne(`UPDATE tasks SET status = ?, completed_at = ? WHERE id = ?`, status, completed, id)
}

// MoveTask re-plans a task for a different date.
func (s *Store) MoveTask(id int64, date string) error {
	return s.execOne(`UPDATE tasks SET date = ? WHERE id = ?`, date, id)
}

func (s *Store) execOne(query string, args ...any) error {
	res, err := s.q.ExecContext(context.Background(), query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) queryTasks(where string, args ...any) ([]Task, error) {
	rows, err := s.q.QueryContext(context.Background(),
		`SELECT id, text, date, status, created_at, COALESCE(completed_at, '') FROM tasks `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Text, &t.Date, &t.Status, &t.CreatedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- state ----

// GetState returns the value for key, or "" if unset.
func (s *Store) GetState(key string) (string, error) {
	var v string
	err := s.q.QueryRowContext(context.Background(), `SELECT value FROM state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetState(key, value string) error {
	_, err := s.q.ExecContext(context.Background(),
		`INSERT INTO state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

func (s *Store) DeleteState(key string) error {
	_, err := s.q.ExecContext(context.Background(), `DELETE FROM state WHERE key = ?`, key)
	return err
}

// LastInboundAt returns when the owner last messaged the bot (zero time if never).
func (s *Store) LastInboundAt() (time.Time, error) {
	v, err := s.GetState(KeyLastInboundAt)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, v)
}

// TouchInbound records that the owner just messaged the bot.
func (s *Store) TouchInbound() error {
	return s.SetState(KeyLastInboundAt, s.stamp())
}
