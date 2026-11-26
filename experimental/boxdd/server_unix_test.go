//go:build !windows

package main

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestListenEndpointIsNotWorldAccessible(t *testing.T) {
	previousDirectory := workingDirectory
	previousSocket := socketPath
	workingDirectory = t.TempDir()
	socketPath = ""
	t.Cleanup(func() {
		workingDirectory = previousDirectory
		socketPath = previousSocket
	})

	listener, err := listenEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	path := filepath.Join(workingDirectory, serviceName+".sock")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o002 != 0 {
		t.Fatalf("daemon socket is accessible to other users: %o", info.Mode().Perm())
	}
	wantMode := os.FileMode(0o600)
	if _, err = user.LookupGroup(serviceName); err == nil {
		wantMode = 0o660
	}
	if info.Mode().Perm() != wantMode {
		t.Fatalf("daemon socket mode = %o, want %o", info.Mode().Perm(), wantMode)
	}
}

func TestListenEndpointDoesNotRemoveOrdinaryFile(t *testing.T) {
	previousDirectory := workingDirectory
	previousSocket := socketPath
	workingDirectory = t.TempDir()
	socketPath = filepath.Join(workingDirectory, "daemon.sock")
	t.Cleanup(func() {
		workingDirectory = previousDirectory
		socketPath = previousSocket
	})
	content := []byte("operator data")
	if err := os.WriteFile(socketPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenEndpoint(); !os.IsExist(err) {
		t.Fatalf("ordinary file at socket path must fail with os.ErrExist, got %v", err)
	}
	after, err := os.ReadFile(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(content) {
		t.Fatal("ordinary file at socket path was modified")
	}
}
