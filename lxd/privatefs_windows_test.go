//go:build with_lxd && windows

package lxd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func requirePrivateNode(t *testing.T, path string, directory bool) {
	t.Helper()
	facts, err := readSecurityFacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if !privateNodeInNorm(facts, directory) {
		t.Fatalf("%s is not private: owner=%s protected=%v null=%v entries=%+v", path, principalName(facts.Owner), facts.Protected, facts.NullDACL, facts.DACL)
	}
}

func TestWindowsPrivateStateSecretsAndTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	if err := SaveDaemonConfig(dir, DaemonConfig{Secret: "bearer-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateServerIdentity(dir, time.Now()); err != nil {
		t.Fatal(err)
	}

	requirePrivateNode(t, dir, true)
	requirePrivateNode(t, daemonConfigPath(dir), false)
	requirePrivateNode(t, filepath.Join(dir, "server_key.pem"), false)

	temp, err := createPrivateTemp(dir, ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	tempPath := temp.Name()
	t.Cleanup(func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	})
	// Inspect before writing: the empty atomic temp file must already carry
	// the private DACL, not inherit a world-readable parent ACL until later.
	requirePrivateNode(t, tempPath, false)
	if _, err = temp.Write([]byte("secret")); err != nil {
		t.Fatal(err)
	}
	if err = temp.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPrivateAtomicReplacesExistingDACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := daemonConfigPath(dir)
	// Create the old file before the private directory DACL is applied. Its
	// inherited ACL must not survive the atomic replacement.
	if err := os.WriteFile(path, []byte(`{"secret":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveDaemonConfig(dir, DaemonConfig{Secret: "new"}); err != nil {
		t.Fatal(err)
	}
	requirePrivateNode(t, dir, true)
	requirePrivateNode(t, path, false)
}

func TestWindowsLoadSecuresExistingDaemonConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(daemonConfigPath(dir), []byte(`{"secret":"existing"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, found, err := LoadDaemonConfig(dir)
	if err != nil || !found || config.Secret != "existing" {
		t.Fatalf("load existing config: found=%v config=%+v err=%v", found, config, err)
	}
	requirePrivateNode(t, dir, true)
	requirePrivateNode(t, daemonConfigPath(dir), false)
}

func TestWindowsServerIdentitySecuresExistingKey(t *testing.T) {
	original, err := loadOrCreateServerIdentity(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "server_cert.pem"), original.certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "server_key.pem"), original.keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadOrCreateServerIdentity(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.fingerprint != original.fingerprint {
		t.Fatal("loading the existing identity changed its fingerprint")
	}
	requirePrivateNode(t, dir, true)
	requirePrivateNode(t, filepath.Join(dir, "server_key.pem"), false)
}

func TestWindowsPrivateFileRejectsHardLink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := SaveDaemonConfig(dir, DaemonConfig{Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "daemon-hardlink.json")
	if err := os.Link(daemonConfigPath(dir), link); err != nil {
		t.Skipf("hard link creation is unavailable: %v", err)
	}
	if err := securePrivateFile(link); err == nil {
		t.Fatal("a hard-linked secret must fail closed")
	}
}

func TestWindowsPrivateDirRejectsReparsePoint(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("a reparse point must not be accepted as private state")
	}
}
