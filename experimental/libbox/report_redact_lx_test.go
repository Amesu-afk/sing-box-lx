//go:build darwin || linux || windows

package libbox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRedactConfigCredentialsRecursively(t *testing.T) {
	content := []byte(`{
		"log": {"level": "info"},
		"outbounds": [{
			"type": "vless",
			"tag": "node-de",
			"server": "node.example",
			"server_port": 443,
			"uuid": "uuid-secret",
			"encryption": "encryption-secret",
			"nested": [{"auth-key": "auth-secret", "keep": true}]
		}],
		"credentials": {
			"password": "password-secret",
			"psk": "psk-secret",
			"private_key": "private-key-secret",
			"auth_key": "auth-key-secret",
			"private_key_passphrase": "passphrase-secret",
			"token": "token-secret",
			"secret": "secret-secret",
			"apiKey": "api-key-secret",
			"authorization": "authorization-secret",
			"cookie": "cookie-secret"
		},
		"transport": {
			"headers": {
				"Authorization": "Bearer header-secret",
				"Proxy-Authorization": "proxy-secret",
				"Cookie": "cookie-header-secret",
				"Set-Cookie": "set-cookie-secret",
				"X-API-Key": "api-header-secret",
				"X-Auth-Token": "token-header-secret",
				"X-API-Token": "api-token-header-secret",
				"User-Agent": "keep-agent",
				"Accept": "application/json"
			}
		}
	}`)

	redacted, err := redactConfigCredentials(content)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"uuid-secret", "encryption-secret", "auth-secret", "password-secret",
		"psk-secret", "private-key-secret", "auth-key-secret", "passphrase-secret",
		"token-secret", "secret-secret", "api-key-secret", "authorization-secret",
		"cookie-secret", "header-secret", "proxy-secret", "cookie-header-secret",
		"set-cookie-secret", "api-header-secret", "token-header-secret", "api-token-header-secret",
	} {
		if bytes.Contains(redacted, []byte(secret)) {
			t.Errorf("redacted snapshot still contains %q", secret)
		}
	}

	var snapshot map[string]any
	if err = json.Unmarshal(redacted, &snapshot); err != nil {
		t.Fatal(err)
	}
	outbound := snapshot["outbounds"].([]any)[0].(map[string]any)
	for key, want := range map[string]any{
		"type": "vless", "tag": "node-de", "server": "node.example", "server_port": float64(443),
	} {
		if outbound[key] != want {
			t.Errorf("structural field %s = %#v, want %#v", key, outbound[key], want)
		}
	}
	if outbound["uuid"] != redactedCredential {
		t.Errorf("uuid was not redacted: %#v", outbound["uuid"])
	}
	headers := snapshot["transport"].(map[string]any)["headers"].(map[string]any)
	if headers["User-Agent"] != "keep-agent" || headers["Accept"] != "application/json" {
		t.Fatalf("non-sensitive headers changed: %#v", headers)
	}
	if headers["Authorization"] != redactedCredential {
		t.Fatalf("Authorization was not redacted: %#v", headers["Authorization"])
	}
}

func TestRedactConfigCredentialsRejectsInvalidJSON(t *testing.T) {
	for _, content := range [][]byte{
		[]byte(`{"password": "secret",}`),
		[]byte(`{"password": "secret"} {"uuid": "secret"}`),
	} {
		if redacted, err := redactConfigCredentials(content); err == nil {
			t.Fatalf("invalid snapshot was accepted: %s", redacted)
		}
	}
}

func TestCopyConfigSnapshotRedactsBeforeWriting(t *testing.T) {
	base := t.TempDir()
	destination := filepath.Join(base, "report")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "configuration.json"), []byte(`{
		"outbounds": [{"type": "trojan", "tag": "keep", "password": "do-not-archive"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	previousBase := sBasePath
	sBasePath = base
	t.Cleanup(func() { sBasePath = previousBase })

	copyConfigSnapshot(destination)

	content, err := os.ReadFile(filepath.Join(destination, "configuration.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("do-not-archive")) {
		t.Fatal("configuration snapshot was written before redaction")
	}
	if !bytes.Contains(content, []byte(redactedCredential)) || !bytes.Contains(content, []byte(`"tag": "keep"`)) {
		t.Fatalf("snapshot lost redaction or structural fields: %s", content)
	}
}

func TestCopyConfigSnapshotOmitsInvalidJSON(t *testing.T) {
	base := t.TempDir()
	destination := filepath.Join(base, "report")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "configuration.json"), []byte(`{"password": "do-not-archive",}`), 0o600); err != nil {
		t.Fatal(err)
	}
	previousBase := sBasePath
	sBasePath = base
	t.Cleanup(func() { sBasePath = previousBase })

	copyConfigSnapshot(destination)

	if _, err := os.Stat(filepath.Join(destination, "configuration.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid snapshot must not be copied, stat error: %v", err)
	}
}
