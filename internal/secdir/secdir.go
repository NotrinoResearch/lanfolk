// Package secdir makes the service's data directory private to the service.
//
// On Windows the default data directory lives in %ProgramData%, whose
// inherited ACL lets every local user read files and create new files and
// folders. Go's permission bits do nothing there. A local user could read
// invite codes, or create the directory before Lanfolk is installed, own it,
// and plant junctions that the service (LocalSystem) then follows. Harden
// replaces the directory's security with an explicit, protected one and
// removes links that the service must never follow.
package secdir

// Harden makes dir (created if missing) accessible only to LocalSystem and
// Administrators and owned by Administrators. Reparse points (junctions,
// symbolic links) and files with more than one hard link inside it are
// removed. It returns a list of what it changed, for the log.
func Harden(dir string) ([]string, error) { return harden(dir) }
