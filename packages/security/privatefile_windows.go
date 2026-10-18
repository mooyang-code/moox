//go:build windows

package security

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// checkPrivateAccess 读取文件的 DACL：只允许当前用户、SYSTEM 与 Administrators 的允许项；
// 没有 DACL（任何人都可访问）或出现其他账户、组（Everyone、Users、Authenticated Users 等）都拒绝。拒绝项不影响判断。
func checkPrivateAccess(path string, _ os.FileInfo) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read ACL of %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read ACL of %s: %w", path, err)
	}
	if dacl == nil {
		return fmt.Errorf("%s has no access control list; restrict it to the current user", path)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("read current user: %w", err)
	}
	allowed := []*windows.SID{user.User.Sid}
	for _, known := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, err := windows.CreateWellKnownSid(known)
		if err != nil {
			return fmt.Errorf("resolve well-known SID: %w", err)
		}
		allowed = append(allowed, sid)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read ACL entry of %s: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		ok := false
		for _, candidate := range allowed {
			if windows.EqualSid(sid, candidate) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%s is accessible by %s; restrict it to the current user (icacls %q /inheritance:r /grant:r \"%%USERNAME%%:F\")", path, sid.String(), path)
		}
	}
	return nil
}
