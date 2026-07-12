// Copyright (C) 2024-2026 American Code
// SPDX-License-Identifier: AGPL-3.0-or-later
// For commercial licensing: jmelton@americancode.org

package federation

import (
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite" // pure-Go driver, same as internal/settlement
)

// ErrForkedHistory means a peer signed two different events at a sequence
// number this store already has on file — the peer rewrote its own signed
// history, which Ed25519 makes provable rather than a matter of whose word
// to take.
var ErrForkedHistory = fmt.Errorf("federation: peer resigned a different event at a previously-witnessed sequence number")

// ErrKeyMismatch means the event's signing key doesn't match the key this
// store already pinned for that pod_id — the same impersonation check
// directory.PinStore does for PodHealthDigest, applied here to ledger events.
var ErrKeyMismatch = fmt.Errorf("federation: event signing key does not match the key pinned for this pod_id")

// Store persists this pod's own signed credit-issuance history (so peers can
// pull it even across a restart), the witnessed copies of peer pods'
// histories this coordinator has verified and stored, and the pinned
// public key for each known peer pod_id.
//
// db is nil for a pure in-memory store (tests, or a coordinator run without
// --db-path) — federation witnessing simply doesn't survive a restart in that
// mode, same tradeoff the ledger itself makes without persistence.
type Store struct {
	mu         sync.Mutex
	db         *sql.DB
	selfEvents []LedgerEvent            // in-memory mirror, ordered by sequence
	nextSeq    uint64                   // next sequence number to assign to a self event
	peerKeys   map[string]string        // pod_id -> hex pubkey, TOFU-pinned
	witnessed  map[string][]LedgerEvent // pod_id -> events, ordered by sequence
}

// NewStore opens (creating if needed) a SQLite-backed federation store at
// dbPath, or an in-memory-only store if dbPath is empty.
func NewStore(dbPath string) (*Store, error) {
	s := &Store{
		nextSeq:   1,
		peerKeys:  make(map[string]string),
		witnessed: make(map[string][]LedgerEvent),
	}
	if dbPath == "" {
		return s, nil
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open federation db %s: %w", dbPath, err)
	}
	if err := migrateSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	s.db = db
	if err := s.loadFromDB(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func migrateSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS federation_self_events (
			sequence   INTEGER PRIMARY KEY,
			event_type TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			amount     REAL NOT NULL,
			issued_at  TEXT NOT NULL,
			public_key TEXT NOT NULL,
			signature  TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS federation_witnessed_events (
			pod_id     TEXT NOT NULL,
			sequence   INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			user_id    TEXT NOT NULL,
			amount     REAL NOT NULL,
			issued_at  TEXT NOT NULL,
			public_key TEXT NOT NULL,
			signature  TEXT NOT NULL,
			PRIMARY KEY (pod_id, sequence)
		);
		CREATE TABLE IF NOT EXISTS federation_peer_keys (
			pod_id     TEXT PRIMARY KEY,
			public_key TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_witnessed_user ON federation_witnessed_events(pod_id, user_id);
	`)
	if err != nil {
		return fmt.Errorf("migrate federation schema: %w", err)
	}
	return nil
}

func (s *Store) loadFromDB() error {
	rows, err := s.db.Query(`SELECT sequence, event_type, user_id, amount, issued_at, public_key, signature FROM federation_self_events ORDER BY sequence`)
	if err != nil {
		return fmt.Errorf("load self events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e LedgerEvent
		if err := rows.Scan(&e.Sequence, &e.EventType, &e.UserID, &e.Amount, &e.IssuedAt, &e.PublicKey, &e.Signature); err != nil {
			return fmt.Errorf("scan self event: %w", err)
		}
		s.selfEvents = append(s.selfEvents, e)
		if e.Sequence >= s.nextSeq {
			s.nextSeq = e.Sequence + 1
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	wRows, err := s.db.Query(`SELECT pod_id, sequence, event_type, user_id, amount, issued_at, public_key, signature FROM federation_witnessed_events ORDER BY pod_id, sequence`)
	if err != nil {
		return fmt.Errorf("load witnessed events: %w", err)
	}
	defer wRows.Close()
	for wRows.Next() {
		var e LedgerEvent
		if err := wRows.Scan(&e.PodID, &e.Sequence, &e.EventType, &e.UserID, &e.Amount, &e.IssuedAt, &e.PublicKey, &e.Signature); err != nil {
			return fmt.Errorf("scan witnessed event: %w", err)
		}
		s.witnessed[e.PodID] = append(s.witnessed[e.PodID], e)
	}
	if err := wRows.Err(); err != nil {
		return err
	}

	keyRows, err := s.db.Query(`SELECT pod_id, public_key FROM federation_peer_keys`)
	if err != nil {
		return fmt.Errorf("load peer keys: %w", err)
	}
	defer keyRows.Close()
	for keyRows.Next() {
		var podID, key string
		if err := keyRows.Scan(&podID, &key); err != nil {
			return fmt.Errorf("scan peer key: %w", err)
		}
		s.peerKeys[podID] = key
	}
	return keyRows.Err()
}

// NextSequence returns the sequence number the next self event must use.
func (s *Store) NextSequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextSeq
}

// AppendSelfEvent persists an already-signed event as the next entry in this
// pod's own history. Callers build evt with Sequence == NextSequence() and
// sign it before calling this — signing after assignment (not before) means
// a concurrent racing append can't invalidate an in-flight signature, since
// the sequence is read and the row inserted under the same lock here.
func (s *Store) AppendSelfEvent(evt LedgerEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if evt.Sequence != s.nextSeq {
		return fmt.Errorf("federation: expected sequence %d, got %d (concurrent append?)", s.nextSeq, evt.Sequence)
	}
	if s.db != nil {
		_, err := s.db.Exec(
			`INSERT INTO federation_self_events (sequence, event_type, user_id, amount, issued_at, public_key, signature) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			evt.Sequence, evt.EventType, evt.UserID, evt.Amount, evt.IssuedAt, evt.PublicKey, evt.Signature,
		)
		if err != nil {
			return fmt.Errorf("persist self event: %w", err)
		}
	}
	s.selfEvents = append(s.selfEvents, evt)
	s.nextSeq++
	return nil
}

// SelfEventsSince returns this pod's own events with Sequence > since, for
// serving GET /federation/ledger-events to a polling peer.
func (s *Store) SelfEventsSince(since uint64) []LedgerEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []LedgerEvent
	for _, e := range s.selfEvents {
		if e.Sequence > since {
			out = append(out, e)
		}
	}
	return out
}

// PinnedPeerKey returns the pinned hex public key for podID, if any.
func (s *Store) PinnedPeerKey(podID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.peerKeys[podID]
	return k, ok
}

// pinPeerKeyLocked pins podID's key on first sight (TOFU), persisting it.
// Must be called with s.mu held.
func (s *Store) pinPeerKeyLocked(podID, pubKeyHex string) error {
	if s.db != nil {
		_, err := s.db.Exec(
			`INSERT INTO federation_peer_keys (pod_id, public_key) VALUES (?, ?)`,
			podID, pubKeyHex,
		)
		if err != nil {
			return fmt.Errorf("persist peer key pin: %w", err)
		}
	}
	s.peerKeys[podID] = pubKeyHex
	return nil
}

// StoreWitnessedEvent verifies evt's signature, checks its signing key
// against the pinned key for evt.PodID (pinning it on first sight), and
// stores it. Returns ErrKeyMismatch if a different pod is now claiming to be
// evt.PodID, or ErrForkedHistory if evt.PodID previously signed a DIFFERENT
// event at the same sequence number — both are hard evidence of misbehavior,
// not ambiguous signals. Re-witnessing the exact same event already on file
// is a no-op, not an error (peers are polled repeatedly by design).
func (s *Store) StoreWitnessedEvent(evt LedgerEvent) error {
	if _, ok := VerifySelfConsistent(evt); !ok {
		return fmt.Errorf("federation: event for pod %q has missing or invalid signature", evt.PodID)
	}
	pubHex := evt.PublicKey

	s.mu.Lock()
	defer s.mu.Unlock()

	if pinned, known := s.peerKeys[evt.PodID]; known {
		if pinned != pubHex {
			return ErrKeyMismatch
		}
	} else if err := s.pinPeerKeyLocked(evt.PodID, pubHex); err != nil {
		return err
	}

	for _, existing := range s.witnessed[evt.PodID] {
		if existing.Sequence != evt.Sequence {
			continue
		}
		if existing.Signature == evt.Signature {
			return nil // already have this exact event; idempotent
		}
		return ErrForkedHistory
	}

	if s.db != nil {
		_, err := s.db.Exec(
			`INSERT INTO federation_witnessed_events (pod_id, sequence, event_type, user_id, amount, issued_at, public_key, signature) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			evt.PodID, evt.Sequence, evt.EventType, evt.UserID, evt.Amount, evt.IssuedAt, evt.PublicKey, evt.Signature,
		)
		if err != nil {
			return fmt.Errorf("persist witnessed event: %w", err)
		}
	}
	s.witnessed[evt.PodID] = append(s.witnessed[evt.PodID], evt)
	return nil
}

// WitnessedHighWatermark returns the highest sequence number witnessed for
// podID so far, for the poll loop to resume from (GET .../ledger-events?since=N).
func (s *Store) WitnessedHighWatermark(podID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var max uint64
	for _, e := range s.witnessed[podID] {
		if e.Sequence > max {
			max = e.Sequence
		}
	}
	return max
}

// WitnessedGrossCredits sums every witnessed credit event for (podID, userID)
// — the amount that pod has ever SIGNED as credited to that user, regardless
// of what it has since been spent on. A live balance query from that pod
// exceeding this sum is proof the pod is claiming more than its own signed
// history backs (see cmd/coordinator's GET /federation/audit/{user_id}).
func (s *Store) WitnessedGrossCredits(podID, userID string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total float64
	for _, e := range s.witnessed[podID] {
		if e.UserID == userID {
			total += e.Amount
		}
	}
	return total
}
