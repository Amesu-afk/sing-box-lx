//go:build with_lxd && windows

package lxd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
)

// Unix mode bits do not create a Windows DACL. Runtime state is therefore
// private to the process identity, SYSTEM and Administrators, with inheritance
// cut off on every node. In particular, no Authenticated Users or Everyone
// entry may survive from ProgramData or a temporary parent directory.
var (
	privateUserSIDOnce sync.Once
	privateUserSID     string
	privateUserSIDErr  error
)

func currentPrivateUserSID() (string, error) {
	privateUserSIDOnce.Do(func() {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			privateUserSIDErr = E.Cause(err, "read the current process user")
			return
		}
		if user == nil || user.User.Sid == nil {
			privateUserSIDErr = errors.New("the current process has no user SID")
			return
		}
		privateUserSID = user.User.Sid.String()
		if privateUserSID == "" {
			privateUserSIDErr = errors.New("the current process user SID is invalid")
		}
	})
	return privateUserSID, privateUserSIDErr
}

func privatePrincipals() ([]string, error) {
	userSID, err := currentPrivateUserSID()
	if err != nil {
		return nil, err
	}
	principals := make([]string, 0, 3)
	seen := make(map[string]bool, 3)
	for _, sid := range []string{userSID, sidSystem, sidAdministrators} {
		if !seen[sid] {
			seen[sid] = true
			principals = append(principals, sid)
		}
	}
	return principals, nil
}

func privateSDDL(directory bool) (string, error) {
	principals, err := privatePrincipals()
	if err != nil {
		return "", err
	}
	var descriptor strings.Builder
	// Omitting the owner makes Windows assign the process token's user. Some
	// hosts reject an explicit user SID as owner even though the same SID is
	// valid in an ACE; the resulting owner is exactly the identity we want.
	descriptor.WriteString("D:P")
	flags := ""
	if directory {
		flags = "OICI"
	}
	for _, sid := range principals {
		fmt.Fprintf(&descriptor, "(A;%s;FA;;;%s)", flags, sid)
	}
	return descriptor.String(), nil
}

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return E.Cause(err, "create the parent of ", path)
		}
		descriptor, descriptorErr := privateSDDL(true)
		if descriptorErr != nil {
			return descriptorErr
		}
		if err = createProtectedDir(path, descriptor); err != nil {
			return E.Cause(err, "create private state directory ", path)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return E.New(path, ": not a directory")
	}
	return protectPrivateNode(path, true)
}

// securePrivateFile protects an existing secret before it is read. Absence is
// left alone so probing a fileless development state does not create a state
// directory as a side effect.
func securePrivateFile(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	return protectPrivateNode(path, false)
}

func protectPrivateNode(path string, directory bool) error {
	const access = windows.READ_CONTROL | windows.WRITE_DAC
	handle, err := openNoFollow(path, access)
	if err != nil {
		return E.Cause(err, "open ", path)
	}
	defer windows.CloseHandle(handle)
	facts, links, err := securityOfHandle(handle)
	if err != nil {
		return E.Cause(err, "read the security of ", path)
	}
	if !directory && links > 1 {
		return E.New(path, ": has ", links, " hard links; refusing to change its ACL")
	}
	if facts.ReparsePoint {
		return E.New(path, ": is a reparse point; refusing to change its ACL")
	}
	if facts.Directory != directory {
		return E.New(path, ": unexpected file type")
	}
	userSID, err := currentPrivateUserSID()
	if err != nil {
		return err
	}
	if facts.Owner.SID != userSID && facts.Owner.SID != sidSystem && facts.Owner.SID != sidAdministrators {
		return E.New(path, ": owner ", principalName(facts.Owner), " is not the current user, SYSTEM or Administrators")
	}
	if privateNodeInNorm(facts, directory) {
		return nil
	}
	descriptor, err := privateSDDL(directory)
	if err != nil {
		return err
	}
	securityDescriptor, err := windows.SecurityDescriptorFromString(descriptor)
	if err != nil {
		return err
	}
	dacl, _, err := securityDescriptor.DACL()
	if err != nil {
		return err
	}
	// Keep the existing owner. An owner can always replace its DACL but need
	// not hold WRITE_OWNER; a foreign owner for which WRITE_DAC is unavailable
	// fails closed above instead of silently leaving inherited access behind.
	err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		return E.Cause(err, "replace the DACL on ", path)
	}
	facts, _, err = securityOfHandle(handle)
	if err != nil {
		return E.Cause(err, "verify the security of ", path)
	}
	if !privateNodeInNorm(facts, directory) {
		return E.New(path, ": private DACL could not be verified; refusing to write state")
	}
	return nil
}

func privateNodeInNorm(facts securityFacts, directory bool) bool {
	if facts.ReparsePoint || facts.NullDACL || !facts.Protected || facts.Directory != directory {
		return false
	}
	userSID, err := currentPrivateUserSID()
	if err != nil || facts.Owner.SID != userSID && facts.Owner.SID != sidSystem && facts.Owner.SID != sidAdministrators {
		return false
	}
	principals, err := privatePrincipals()
	if err != nil {
		return false
	}
	allowed := make(map[string]bool, len(principals))
	for _, sid := range principals {
		allowed[sid] = true
	}
	flags := uint8(0)
	if directory {
		flags = aceObjectInherit | aceContainerInherit
	}
	full := make(map[string]bool, len(principals))
	for _, entry := range facts.DACL {
		if !allowed[entry.Principal.SID] || entry.Type != aceAccessAllowed || entry.Flags != flags {
			return false
		}
		if entry.Mask&accessFileAll == accessFileAll || entry.Mask&accessGenericAll != 0 {
			full[entry.Principal.SID] = true
		}
	}
	for _, sid := range principals {
		if !full[sid] {
			return false
		}
	}
	return true
}

// createPrivateTemp uses CREATE_NEW with an explicit security descriptor, so
// even the empty temporary file has its final DACL from birth. os.CreateTemp
// would inherit the parent's ACL and leave a window before it could be fixed.
func createPrivateTemp(dir string, pattern string) (*os.File, error) {
	star := strings.LastIndexByte(pattern, '*')
	if star < 0 {
		return nil, errors.New("private temporary file pattern has no wildcard")
	}
	descriptor, err := privateSDDL(false)
	if err != nil {
		return nil, err
	}
	attributes, err := securityAttributes(descriptor)
	if err != nil {
		return nil, err
	}
	for range 10000 {
		var random [10]byte
		if _, err = rand.Read(random[:]); err != nil {
			return nil, E.Cause(err, "create a private temporary file name")
		}
		name := pattern[:star] + hex.EncodeToString(random[:]) + pattern[star+1:]
		path := filepath.Join(dir, name)
		pathName, nameErr := windows.UTF16PtrFromString(path)
		if nameErr != nil {
			return nil, nameErr
		}
		handle, createErr := windows.CreateFile(pathName,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if errors.Is(createErr, windows.ERROR_FILE_EXISTS) || errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if createErr != nil {
			return nil, E.Cause(createErr, "create private temporary file ", path)
		}
		return os.NewFile(uintptr(handle), path), nil
	}
	return nil, errors.New("no private temporary file name is available")
}
