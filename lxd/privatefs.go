//go:build with_lxd

package lxd

import "os"

// writePrivateAtomic writes a state file through a private temporary file in
// the same directory. The platform helpers create both the directory and the
// temporary file with their final access control before any secret is written;
// the rename then carries that access control to the destination.
func writePrivateAtomic(dir string, path string, content []byte) error {
	temp, err := createPrivateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err = temp.Write(content); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err = os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	return nil
}
