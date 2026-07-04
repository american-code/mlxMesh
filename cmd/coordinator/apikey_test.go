package main

import (
	"path/filepath"
	"testing"
)

func TestAPIKeyStore_HashedAtRest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "keys.db")
	s, err := newPersistentAPIKeyStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	key, err := s.generate("alice")
	if err != nil {
		t.Fatal(err)
	}

	// lookup succeeds with the raw key the caller was given.
	if uid, ok := s.lookup(key); !ok || uid != "alice" {
		t.Fatalf("lookup(rawKey) = (%q, %v), want (alice, true)", uid, ok)
	}
	// The raw key itself must never be a valid map/DB key — only its hash is.
	if _, ok := s.byKey[key]; ok {
		t.Error("raw key must not be usable as the byKey map key — only its hash should be stored")
	}

	// Confirm the DB column literally does not contain the raw key.
	var stored string
	if err := s.db.QueryRow(`SELECT api_key FROM api_keys WHERE user_id = ?`, "alice").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == key {
		t.Error("raw API key was stored in the database — must store only the hash")
	}
	if stored != hashAPIKey(key) {
		t.Errorf("stored value = %q, want sha256(key) = %q", stored, hashAPIKey(key))
	}
}

func TestAPIKeyStore_HashedAtRest_InMemory(t *testing.T) {
	s := newAPIKeyStore()
	key, err := s.generate("bob")
	if err != nil {
		t.Fatal(err)
	}
	if uid, ok := s.lookup(key); !ok || uid != "bob" {
		t.Fatalf("lookup(rawKey) = (%q, %v), want (bob, true)", uid, ok)
	}
	if _, ok := s.byKey[key]; ok {
		t.Error("raw key must not be usable as the byKey map key — only its hash should be stored")
	}
}

// Regenerating a key immediately invalidates the old one — this is the
// project's existing rotation mechanism (task: secrets hardening confirms it
// still works once storage is hashed, not just before).
func TestAPIKeyStore_RegenerateInvalidatesOldKey(t *testing.T) {
	s := newAPIKeyStore()
	oldKey, err := s.generate("carol")
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := s.generate("carol")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.lookup(oldKey); ok {
		t.Error("old key should be invalidated after regenerating")
	}
	if uid, ok := s.lookup(newKey); !ok || uid != "carol" {
		t.Errorf("lookup(newKey) = (%q, %v), want (carol, true)", uid, ok)
	}
}

func TestAPIKeyStore_PersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "keys.db")
	s1, err := newPersistentAPIKeyStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s1.generate("dave")
	if err != nil {
		t.Fatal(err)
	}

	s2, err := newPersistentAPIKeyStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if uid, ok := s2.lookup(key); !ok || uid != "dave" {
		t.Fatalf("lookup after reopen = (%q, %v), want (dave, true)", uid, ok)
	}
}
