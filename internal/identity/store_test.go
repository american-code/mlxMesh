package identity

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempIdentityPath points identityPath() at a temp file for the duration
// of the test by overriding HOME, since identityPath() derives from
// os.UserHomeDir().
func withTempIdentityPath(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, configDir, identityFile)
}

func TestLoadOrCreate_GeneratesAndPersists(t *testing.T) {
	path := withTempIdentityPath(t)
	priv1, pub1, err := LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if len(priv1) == 0 || len(pub1) == 0 {
		t.Fatal("expected non-empty keypair")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity file mode = %o, want 0600", perm)
	}

	// Second call loads the SAME keypair rather than regenerating.
	priv2, pub2, err := LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if string(priv1) != string(priv2) || string(pub1) != string(pub2) {
		t.Error("LoadOrCreate should load the persisted keypair, not regenerate")
	}
}

func TestLoadOrCreateECDH_GeneratesAndPersists(t *testing.T) {
	withTempIdentityPath(t)
	priv1, err := LoadOrCreateECDH()
	if err != nil {
		t.Fatal(err)
	}
	if priv1 == nil {
		t.Fatal("expected a non-nil ECDH private key")
	}

	// Second call loads the SAME key rather than regenerating.
	priv2, err := LoadOrCreateECDH()
	if err != nil {
		t.Fatal(err)
	}
	if priv1.Equal(priv2) != true {
		t.Error("LoadOrCreateECDH should load the persisted key, not regenerate")
	}
}

// The ECDH backfill must tighten permissions to 0600 even if the file
// previously existed with looser permissions (e.g. restored from a backup) —
// os.WriteFile alone does NOT change the mode of an already-existing file.
func TestLoadOrCreateECDH_TightensLoosePermissions(t *testing.T) {
	path := withTempIdentityPath(t)
	if _, _, err := LoadOrCreate(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreateECDH(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity file mode after ECDH backfill = %o, want 0600 (should be tightened even from a looser starting mode)", perm)
	}
}

func TestLoadOrCreateECDH_BackfillsExistingEd25519OnlyFile(t *testing.T) {
	withTempIdentityPath(t)
	priv, pub, err := LoadOrCreate() // Ed25519-only identity file, no ECDH key yet
	if err != nil {
		t.Fatal(err)
	}

	ecdhPriv, err := LoadOrCreateECDH()
	if err != nil {
		t.Fatal(err)
	}
	if ecdhPriv == nil {
		t.Fatal("expected a backfilled ECDH key")
	}

	// The Ed25519 identity must be untouched by the ECDH backfill.
	priv2, pub2, err := LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if string(priv) != string(priv2) || string(pub) != string(pub2) {
		t.Error("ECDH backfill must not alter the existing Ed25519 identity")
	}
}
