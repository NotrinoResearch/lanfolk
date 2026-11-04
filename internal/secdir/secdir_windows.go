//go:build windows

package secdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// policy describes who owns and who may use the directory tree. SIDs are in
// SDDL form ("SY", "BA" or "S-1-5-...").
type policy struct {
	owner   string
	allowed []string
}

// servicePolicy is the production policy: Administrators own the tree, and
// only LocalSystem and Administrators have access.
var servicePolicy = policy{owner: "BA", allowed: []string{"SY", "BA"}}

// trustedOwners may own an existing data directory without it being
// reported as taken over.
var trustedOwners = []string{
	"S-1-5-18",     // LocalSystem
	"S-1-5-32-544", // Administrators
	"S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464", // TrustedInstaller
}

func (p policy) sddl(dir bool) string {
	flags := ""
	if dir {
		flags = "OICI"
	}
	var b strings.Builder
	b.WriteString("O:" + p.owner + "D:P")
	for _, sid := range p.allowed {
		fmt.Fprintf(&b, "(A;%s;FA;;;%s)", flags, sid)
	}
	return b.String()
}

var (
	advapi32            = windows.NewLazySystemDLL("advapi32.dll")
	procSetFileSecurity = advapi32.NewProc("SetFileSecurityW")
)

// setFileSecurity sets parts of a file's security descriptor without
// propagating anything to children (unlike SetNamedSecurityInfo, which walks
// the tree and could follow a link planted in it).
func setFileSecurity(path string, info windows.SECURITY_INFORMATION, sd *windows.SECURITY_DESCRIPTOR) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	r, _, e := procSetFileSecurity.Call(uintptr(unsafe.Pointer(p)), uintptr(info), uintptr(unsafe.Pointer(sd)))
	if r == 0 {
		return e
	}
	return nil
}

// apply sets owner and protected DACL in two steps: taking ownership first
// gives the owner's implicit right to change a hostile DACL.
func apply(path string, sd *windows.SECURITY_DESCRIPTOR) error {
	if err := setFileSecurity(path, windows.OWNER_SECURITY_INFORMATION, sd); err != nil {
		return fmt.Errorf("set owner of %s: %w", path, err)
	}
	if err := setFileSecurity(path, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, sd); err != nil {
		return fmt.Errorf("set permissions of %s: %w", path, err)
	}
	return nil
}

type entry struct {
	reparse bool
	dir     bool
	links   uint32
}

// inspect describes path itself, never what a link points to.
func inspect(path string) (entry, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return entry{}, err
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return entry{}, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(h)
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return entry{}, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	return entry{
		reparse: fi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0,
		dir:     fi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0,
		links:   fi.NumberOfLinks,
	}, nil
}

// enablePrivileges turns on the privileges LocalSystem holds but has
// disabled by default, so a directory with a hostile owner and DACL can be
// taken over. Missing privileges (development runs) are not an error.
func enablePrivileges() {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return
	}
	defer tok.Close()
	for _, name := range []string{"SeTakeOwnershipPrivilege", "SeRestorePrivilege", "SeBackupPrivilege"} {
		var luid windows.LUID
		n, _ := windows.UTF16PtrFromString(name)
		if windows.LookupPrivilegeValue(nil, n, &luid) != nil {
			continue
		}
		tp := windows.Tokenprivileges{PrivilegeCount: 1}
		tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
	}
}

func harden(dir string) ([]string, error) { return hardenWith(dir, servicePolicy) }

func hardenWith(dir string, p policy) ([]string, error) {
	dir = filepath.Clean(dir)
	dirSD, err := windows.SecurityDescriptorFromString(p.sddl(true))
	if err != nil {
		return nil, err
	}
	fileSD, err := windows.SecurityDescriptorFromString(p.sddl(false))
	if err != nil {
		return nil, err
	}
	enablePrivileges()
	var changes []string

	e, err := inspect(dir)
	switch {
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return changes, create(dir, dirSD)
	case err != nil:
		return nil, err
	case e.reparse || !e.dir:
		// The data directory itself is a link or a file: move it aside (a
		// rename moves the link, not its target) and start a fresh one.
		aside := fmt.Sprintf("%s.untrusted-%d", dir, time.Now().Unix())
		if err := os.Rename(dir, aside); err != nil {
			return nil, fmt.Errorf("move unsafe data folder aside: %w", err)
		}
		changes = append(changes, "data folder was a link or file; moved to "+aside)
		return changes, create(dir, dirSD)
	}

	if owner := ownerOf(dir); owner != "" && !trusted(owner, p) {
		changes = append(changes, "data folder was owned by "+owner+"; ownership taken")
	}
	if err := apply(dir, dirSD); err != nil {
		return changes, err
	}
	err = walk(dir, dirSD, fileSD, &changes)
	return changes, err
}

func create(dir string, sd *windows.SECURITY_DESCRIPTOR) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(p, &sa); err != nil {
		return &os.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return nil
}

// walk applies the policy below dir. The service never creates links, so
// every reparse point is removed (the link only, never its target), and so
// is every file with more than one hard link: changing its security would
// change the file it is linked to.
func walk(dir string, dirSD, fileSD *windows.SECURITY_DESCRIPTOR, changes *[]string) error {
	names, err := readNames(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		e, err := inspect(path)
		if err != nil {
			return err
		}
		if e.reparse || (!e.dir && e.links > 1) {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove unsafe link %s: %w", path, err)
			}
			*changes = append(*changes, "removed link "+path)
			continue
		}
		sd := fileSD
		if e.dir {
			sd = dirSD
		}
		if err := apply(path, sd); err != nil {
			return err
		}
		if e.dir {
			if err := walk(path, dirSD, fileSD, changes); err != nil {
				return err
			}
		}
	}
	return nil
}

// readNames lists a directory without following anything in it.
func readNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

func ownerOf(path string) string {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return ""
	}
	o, _, err := sd.Owner()
	if err != nil || o == nil {
		return ""
	}
	return o.String()
}

func trusted(owner string, p policy) bool {
	for _, t := range trustedOwners {
		if owner == t {
			return true
		}
	}
	if sid, err := windows.StringToSid(p.owner); err == nil && sid.String() == owner {
		return true
	}
	return false
}
