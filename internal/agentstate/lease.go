package agentstate

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// Lease takes or refreshes an agent's hold on one stage of a scope.
//
// The takeover path is the point of the whole table: when an agent dies
// mid-stage its lease stops being heartbeated, and once the TTL lapses a fresh
// agent may lease the stage. The result then names the agent it displaced and
// the state keys that agent left behind, so the successor resumes from what was
// already learned instead of starting the stage over.
func (s *SQLiteStore) Lease(ctx context.Context, req LeaseRequest) (LeaseResult, error) {
	normScope, err := s.validateLeaseRequest(&req)
	if err != nil {
		return LeaseResult{}, err
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	now := s.now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LeaseResult{}, errors.WrapWithDetails(err, "begin lease transaction")
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := readLease(ctx, tx, normScope, req.Stage)
	if err != nil {
		return LeaseResult{}, err
	}
	if found && blocksLease(existing, req, now) {
		return LeaseResult{Granted: false, Lease: existing}, nil
	}

	granted := Lease{
		Scope:       normScope,
		Stage:       req.Stage,
		Agent:       req.Meta.Agent,
		RunID:       req.Meta.RunID,
		TTL:         ttl,
		LeasedAt:    leaseStart(existing, found, req.Meta.Agent, now),
		HeartbeatAt: now,
	}
	if err := writeLease(ctx, tx, granted); err != nil {
		return LeaseResult{}, err
	}

	result := LeaseResult{Granted: true, Lease: granted}
	if displaced, ok := displacedBy(existing, found, req.Meta.Agent); ok {
		result.Displaced = &displaced
		keys, err := inheritedKeys(ctx, tx, normScope, displaced.Agent)
		if err != nil {
			return LeaseResult{}, err
		}
		result.InheritedKeys = keys
	}

	if err := tx.Commit(); err != nil {
		return LeaseResult{}, errors.WrapWithDetails(err, "commit lease transaction")
	}
	return result, nil
}

// validateLeaseRequest normalises the scope and rejects a lease that could not
// be attributed — an anonymous lease could never be taken over safely.
func (s *SQLiteStore) validateLeaseRequest(req *LeaseRequest) (string, error) {
	normScope, err := NormalizeScope(req.Scope)
	if err != nil {
		return "", err
	}
	if err := ValidateStage(req.Stage); err != nil {
		return "", err
	}
	if req.Meta.Agent == "" {
		return "", errors.WithDetails("lease requires an agent name", "scope", normScope, "stage", req.Stage)
	}
	return normScope, nil
}

// blocksLease reports whether an existing lease stands in the way: only a live
// lease held by a different agent does, and an explicit takeover overrides it.
func blocksLease(existing Lease, req LeaseRequest, now time.Time) bool {
	if req.Takeover || existing.Agent == req.Meta.Agent {
		return false
	}
	return isLive(existing, now)
}

// isLive reports whether a lease is still held: not released, and heartbeated
// within the TTL the holder itself declared.
func isLive(c Lease, now time.Time) bool {
	if c.ReleasedAt != nil {
		return false
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	return now.Sub(c.HeartbeatAt) <= ttl
}

// leaseStart preserves the original lease time when the same agent heartbeats,
// so stage duration measures the work rather than the last heartbeat.
func leaseStart(existing Lease, found bool, agent string, now time.Time) time.Time {
	if found && existing.Agent == agent && existing.ReleasedAt == nil {
		return existing.LeasedAt
	}
	return now
}

// displacedBy reports the previous holder when a different agent takes the
// stage over. A released lease is not a displacement — its agent handed off.
func displacedBy(existing Lease, found bool, agent string) (Lease, bool) {
	if !found || existing.Agent == agent || existing.ReleasedAt != nil {
		return Lease{}, false
	}
	return existing, true
}

// Release marks a stage lease as handed back. When agent is non-empty the
// release only applies to that agent's lease, so a stale process cannot release
// its successor's hold.
func (s *SQLiteStore) Release(ctx context.Context, scope, stage, agent string) (bool, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return false, err
	}
	if err := ValidateStage(stage); err != nil {
		return false, err
	}

	q := `UPDATE agent_leases SET released_at = ? WHERE scope = ? AND stage = ? AND released_at IS NULL`
	args := []any{s.now().UTC().Format(TimeFormat), normScope, stage}
	if agent != "" {
		q += ` AND agent = ?`
		args = append(args, agent)
	}

	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, errors.WrapWithDetails(err, "release lease", "scope", normScope, "stage", stage)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Leases lists every lease on a scope, newest heartbeat first, so an operator
// can see who holds what.
func (s *SQLiteStore) Leases(ctx context.Context, scope string) ([]Lease, error) {
	normScope, err := NormalizeScope(scope)
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT scope, stage, agent, run_id, ttl_seconds, leased_at, heartbeat_at, released_at
		FROM agent_leases WHERE scope = ? ORDER BY heartbeat_at DESC
	`
	rows, err := s.db.QueryContext(ctx, q, normScope)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "list leases", "scope", normScope)
	}
	defer func() { _ = rows.Close() }()

	leases := []Lease{}
	for rows.Next() {
		c, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		leases = append(leases, c)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "read leases", "scope", normScope)
	}
	return leases, nil
}

func readLease(ctx context.Context, tx *sql.Tx, scope, stage string) (Lease, bool, error) {
	const q = `
		SELECT scope, stage, agent, run_id, ttl_seconds, leased_at, heartbeat_at, released_at
		FROM agent_leases WHERE scope = ? AND stage = ?
	`
	c, err := scanLease(tx.QueryRowContext(ctx, q, scope, stage))
	if err != nil {
		if stderrors.Is(err, ErrNotFound) {
			return Lease{}, false, nil
		}
		return Lease{}, false, err
	}
	return c, true, nil
}

func writeLease(ctx context.Context, tx *sql.Tx, c Lease) error {
	const q = `
		INSERT INTO agent_leases (scope, stage, agent, run_id, ttl_seconds, leased_at, heartbeat_at, released_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(scope, stage) DO UPDATE SET
			agent        = excluded.agent,
			run_id       = excluded.run_id,
			ttl_seconds  = excluded.ttl_seconds,
			leased_at   = excluded.leased_at,
			heartbeat_at = excluded.heartbeat_at,
			released_at  = NULL
	`
	_, err := tx.ExecContext(ctx, q, c.Scope, c.Stage, c.Agent, c.RunID, int64(c.TTL/time.Second),
		c.LeasedAt.Format(TimeFormat), c.HeartbeatAt.Format(TimeFormat))
	if err != nil {
		return errors.WrapWithDetails(err, "write lease", "scope", c.Scope, "stage", c.Stage)
	}
	return nil
}

// inheritedKeys lists the state a displaced agent left behind in the scope —
// the successor's inheritance.
func inheritedKeys(ctx context.Context, tx *sql.Tx, scope, agent string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT name FROM agent_state WHERE scope = ? AND agent = ? ORDER BY name`, scope, agent)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "list inherited keys", "scope", scope, "agent", agent)
	}
	defer func() { _ = rows.Close() }()

	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, errors.WrapWithDetails(err, "scan inherited key")
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.WrapWithDetails(err, "read inherited keys", "scope", scope)
	}
	return names, nil
}

func scanLease(sc rowScanner) (Lease, error) {
	var c Lease
	var claimedAt, heartbeatAt string
	var releasedAt sql.NullString
	var ttlSeconds int64
	if err := sc.Scan(&c.Scope, &c.Stage, &c.Agent, &c.RunID, &ttlSeconds,
		&claimedAt, &heartbeatAt, &releasedAt); err != nil {
		if isNoRows(err) {
			return Lease{}, ErrNotFound
		}
		return Lease{}, errors.WrapWithDetails(err, "scan lease")
	}
	c.TTL = time.Duration(ttlSeconds) * time.Second

	var err error
	if c.LeasedAt, err = time.Parse(TimeFormat, claimedAt); err != nil {
		return Lease{}, errors.WrapWithDetails(err, "parse lease timestamp", "value", claimedAt)
	}
	if c.HeartbeatAt, err = time.Parse(TimeFormat, heartbeatAt); err != nil {
		return Lease{}, errors.WrapWithDetails(err, "parse heartbeat timestamp", "value", heartbeatAt)
	}
	if releasedAt.Valid {
		ts, parseErr := time.Parse(TimeFormat, releasedAt.String)
		if parseErr != nil {
			return Lease{}, errors.WrapWithDetails(parseErr, "parse release timestamp", "value", releasedAt.String)
		}
		c.ReleasedAt = &ts
	}
	return c, nil
}

// isNoRows recognises the driver's empty-result error through any wrapping.
func isNoRows(err error) bool {
	return stderrors.Is(err, sql.ErrNoRows)
}
