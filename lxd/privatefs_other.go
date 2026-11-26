//go:build with_lxd && !windows

package lxd

import "os"

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func createPrivateTemp(dir string, pattern string) (*os.File, error) {
	return os.CreateTemp(dir, pattern)
}

func securePrivateFile(_ string) error {
	return nil
}
