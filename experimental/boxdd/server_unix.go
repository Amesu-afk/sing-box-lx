//go:build !windows

package main

import (
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// lx:begin boxdd-socket-permissions
func listenEndpoint() (net.Listener, error) {
	path := socketPath
	if path == "" {
		path = filepath.Join(workingDirectory, serviceName+".sock")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, os.ErrExist
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	closeWithError := func(err error) (net.Listener, error) {
		_ = listener.Close()
		return nil, err
	}
	mode := os.FileMode(0o600)
	group, groupErr := user.LookupGroup(serviceName)
	if groupErr == nil {
		groupID, parseErr := strconv.Atoi(group.Gid)
		if parseErr != nil {
			return closeWithError(parseErr)
		}
		if err = os.Chown(path, -1, groupID); err != nil {
			return closeWithError(err)
		}
		mode = 0o660
	}
	// Without the dedicated group the socket is owner-pinned. In neither case
	// may an unrelated local UID connect and become the first service owner.
	if err = os.Chmod(path, mode); err != nil {
		return closeWithError(err)
	}
	return listener, nil
}

// lx:end boxdd-socket-permissions
