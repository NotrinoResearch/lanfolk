//go:build windows

package secdir

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// testPolicy uses the current user as owner, so the tests run without
// administrator rights. The production policy only differs in the SIDs.
func testPolicy(t *testing.T) policy {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return policy{owner: u.User.Sid.String(), allowed: []string{"SY", u.User.Sid.String()}}
}

func junction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink: %v: %s", err, out)
	}
}

// checkPrivate asserts that path has a protected DACL with exactly the
// policy's entries and the policy's owner.
func checkPrivate(t *testing.T, path string, p policy) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if ctl&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("%s: DACL not protected: %s", path, sd)
	}
	owner, _, _ := sd.Owner()
	if owner.String() != p.owner {
		t.Errorf("%s: owner %s, want %s", path, owner, p.owner)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if int(dacl.AceCount) != len(p.allowed) {
		t.Errorf("%s: %d ACEs, want %d: %s", path, dacl.AceCount, len(p.allowed), sd)
	}
	s := sd.String()
	for _, broad := range []string{";;;BU)", ";;;AU)", ";;;WD)", ";;;IU)"} {
		if strings.Contains(s, broad) {
			t.Errorf("%s: still grants %s: %s", path, broad, s)
		}
	}
}

func TestHardenCreatesPrivateDir(t *testing.T) {
	p := testPolicy(t)
	dir := filepath.Join(t.TempDir(), "Lanfolk")
	changes, err := hardenWith(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %v", changes)
	}
	checkPrivate(t, dir, p)
	// Files created later inherit the private ACL.
	f := filepath.Join(dir, "lanfolk.json")
	if err := os.WriteFile(f, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	sd, _ := windows.GetNamedSecurityInfo(f, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if strings.Contains(sd.String(), ";;;BU)") {
		t.Errorf("new file grants Users: %s", sd)
	}
}

func TestHardenExistingTree(t *testing.T) {
	p := testPolicy(t)
	base := t.TempDir()
	dir := filepath.Join(base, "Lanfolk")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(dir, "logs"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(dir, "lanfolk.json"):          "{}",
		filepath.Join(dir, "logs", "service.log"):   "log",
		filepath.Join(outside, "victim.txt"):        "victim",
		filepath.Join(outside, "linked-victim.txt"): "linked",
	}
	for f, c := range files {
		if err := os.WriteFile(f, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	victimSD, _ := windows.GetNamedSecurityInfo(outside, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	// Planted links: a junction in place of a log folder, one inside logs,
	// and a hard link to a file outside the tree.
	junction(t, filepath.Join(dir, "logs2"), outside)
	junction(t, filepath.Join(dir, "logs", "nested"), outside)
	if err := os.Link(filepath.Join(outside, "linked-victim.txt"), filepath.Join(dir, "logs", "service.log.1")); err != nil {
		t.Fatal(err)
	}

	changes, err := hardenWith(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Errorf("changes = %q, want 3 removed links", changes)
	}
	for _, gone := range []string{"logs2", filepath.Join("logs", "nested"), filepath.Join("logs", "service.log.1")} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err %v)", gone, err)
		}
	}
	// Link targets are untouched.
	for _, f := range []string{"victim.txt", "linked-victim.txt"} {
		if _, err := os.Stat(filepath.Join(outside, f)); err != nil {
			t.Errorf("link target %s: %v", f, err)
		}
	}
	afterSD, _ := windows.GetNamedSecurityInfo(outside, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if victimSD.String() != afterSD.String() {
		t.Errorf("outside folder ACL changed:\n%s\n%s", victimSD, afterSD)
	}
	for _, path := range []string{dir, filepath.Join(dir, "logs"), filepath.Join(dir, "lanfolk.json"), filepath.Join(dir, "logs", "service.log")} {
		checkPrivate(t, path, p)
	}
	// Idempotent: a second run changes nothing.
	if changes, err := hardenWith(dir, p); err != nil || len(changes) != 0 {
		t.Errorf("second run: %v, %v", changes, err)
	}
}

func TestHardenDataDirIsJunction(t *testing.T) {
	p := testPolicy(t)
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "Lanfolk")
	junction(t, dir, target)
	changes, err := hardenWith(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || !strings.Contains(changes[0], "moved to") {
		t.Errorf("changes = %q", changes)
	}
	e, err := inspect(dir)
	if err != nil || e.reparse || !e.dir {
		t.Fatalf("data dir after harden: %+v, %v", e, err)
	}
	checkPrivate(t, dir, p)
	if _, err := os.Stat(filepath.Join(target, "keep.txt")); err != nil {
		t.Errorf("junction target damaged: %v", err)
	}
	if names, _ := readNames(dir); len(names) != 0 {
		t.Errorf("new data dir is not empty: %v", names)
	}
}

func TestServicePolicySDDL(t *testing.T) {
	if got, want := servicePolicy.sddl(true), "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"; got != want {
		t.Errorf("dir SDDL %q, want %q", got, want)
	}
	if got, want := servicePolicy.sddl(false), "O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)"; got != want {
		t.Errorf("file SDDL %q, want %q", got, want)
	}
	for _, s := range []string{servicePolicy.sddl(true), servicePolicy.sddl(false)} {
		if _, err := windows.SecurityDescriptorFromString(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	if !trusted("S-1-5-18", servicePolicy) || !trusted("S-1-5-32-544", servicePolicy) || trusted("S-1-5-21-1-2-3-1001", servicePolicy) {
		t.Error("trusted owners wrong")
	}
}
