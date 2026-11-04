package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
	"github.com/NotrinoResearch/lanfolk/internal/version"
)

// The update check asks GitHub for the list of releases: it sends nothing
// but an HTTP request with a generic User-Agent, and only from the desktop
// app (the service never contacts GitHub). Settings > General turns it off.
const (
	releasesAPI  = "https://api.github.com/repos/NotrinoResearch/lanfolk/releases?per_page=20"
	releasesPage = "https://github.com/NotrinoResearch/lanfolk/releases"

	eventUpdate = "update" // UpdateInfo

	updateFirstDelay = 20 * time.Second // let sign-in settle first
	updateEvery      = 24 * time.Hour
	updateRetry      = time.Hour // after a failed or skipped check
	updateTimeout    = 15 * time.Second
	maxReleasesBody  = 2 << 20
)

// UpdateInfo is the payload of the "update" event.
type UpdateInfo struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"` // newest release, if known
	URL       string    `json:"url,omitempty"`    // that release's page
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

type ghRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// semver is MAJOR.MINOR.PATCH with an optional pre-release ("-rc.1").
type semver struct {
	n   [3]int
	pre string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i] // build metadata does not count
	}
	var v semver
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core, v.pre = s[:i], s[i+1:]
		if v.pre == "" {
			return semver{}, false
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" {
			return semver{}, false
		}
		v.n[i] = n
	}
	return v, true
}

// compare returns -1, 0 or 1. A pre-release is older than its release;
// pre-releases compare by their dot-separated identifiers (SemVer 2.0 §11).
func (a semver) compare(b semver) int {
	for i := range a.n {
		if a.n[i] != b.n[i] {
			if a.n[i] < b.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	ap, bp := strings.Split(a.pre, "."), strings.Split(b.pre, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if ap[i] == bp[i] {
			continue
		}
		an, aerr := strconv.Atoi(ap[i])
		bn, berr := strconv.Atoi(bp[i])
		switch {
		case aerr == nil && berr == nil:
			if an < bn {
				return -1
			}
			return 1
		case aerr == nil:
			return -1 // numeric identifiers sort first
		case berr == nil:
			return 1
		case ap[i] < bp[i]:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(ap) < len(bp):
		return -1
	case len(ap) > len(bp):
		return 1
	}
	return 0
}

// newestRelease picks the highest version among published releases.
// Pre-releases count while the app itself is a 0.x or pre-release build:
// that is what testers install.
func newestRelease(rels []ghRelease, current semver) (ghRelease, semver, bool) {
	takePre := current.n[0] == 0 || current.pre != ""
	var best ghRelease
	var bestV semver
	found := false
	for _, r := range rels {
		if r.Draft || (r.Prerelease && !takePre) {
			continue
		}
		v, ok := parseSemver(r.TagName)
		if !ok {
			continue
		}
		if !found || v.compare(bestV) > 0 {
			best, bestV, found = r, v, true
		}
	}
	return best, bestV, found
}

// checkForUpdate asks url for the release list and compares it with
// current.
func checkForUpdate(ctx context.Context, client *http.Client, url, current string) UpdateInfo {
	info := UpdateInfo{Current: current, CheckedAt: time.Now().UTC()}
	cur, ok := parseSemver(current)
	if !ok {
		info.Error = "this build has no release version"
		return info
	}
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Lanfolk/"+current)
	resp, err := client.Do(req)
	if err != nil {
		info.Error = "could not reach GitHub"
		return info
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		info.Error = fmt.Sprintf("GitHub answered %s", resp.Status)
		return info
	}
	var rels []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleasesBody)).Decode(&rels); err != nil {
		info.Error = "unexpected answer from GitHub"
		return info
	}
	best, bestV, found := newestRelease(rels, cur)
	if !found {
		return info
	}
	info.Latest = strings.TrimPrefix(best.TagName, "v")
	info.URL = best.HTMLURL
	if !strings.HasPrefix(info.URL, "https://github.com/") {
		info.URL = releasesPage // never open anything else
	}
	info.Available = bestV.compare(cur) > 0
	return info
}

// updateLoop checks once after start and then daily, while the setting
// allows it. Dev builds never check by themselves.
func (a *App) updateLoop(ctx context.Context) {
	if _, ok := parseSemver(version.Version); !ok || strings.Contains(version.Version, "dev") {
		return
	}
	wait := a.updateFirst
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		wait = updateEvery
		on, known := a.updateChecksOn(ctx)
		if !known {
			wait = updateRetry // ask the service again later
			continue
		}
		if !on {
			continue
		}
		if info := a.runUpdateCheck(ctx); info.Error != "" {
			wait = updateRetry
		}
	}
}

// updateChecksOn reads the setting from the service.
func (a *App) updateChecksOn(ctx context.Context) (on, known bool) {
	a.mu.RLock()
	c := a.client
	a.mu.RUnlock()
	if c == nil {
		return false, false
	}
	cctx, cancel := context.WithTimeout(ctx, a.callTimeout)
	defer cancel()
	var s ipc.Settings
	if err := c.Call(cctx, ipc.MethodGetSettings, nil, &s); err != nil {
		return false, false
	}
	return s.CheckUpdates, true
}

func (a *App) runUpdateCheck(ctx context.Context) UpdateInfo {
	info := checkForUpdate(ctx, a.httpClient, a.updateURL, version.Version)
	a.mu.Lock()
	// A failed check keeps what an earlier one found.
	if info.Error != "" && a.update != nil && a.update.Latest != "" {
		keep := *a.update
		keep.Error, keep.CheckedAt = info.Error, info.CheckedAt
		info = keep
	}
	a.update = &info
	a.mu.Unlock()
	a.host.Emit(eventUpdate, info)
	return info
}

// CheckForUpdates checks right away (Settings > About). It works even when
// automatic checks are off, because the user asked.
func (a *App) CheckForUpdates() (UpdateInfo, error) {
	info := a.runUpdateCheck(context.Background())
	if info.Error != "" && info.Latest == "" {
		return info, &UserError{Code: CodeLocal, Message: "Could not check for updates: " + info.Error + "."}
	}
	return info, nil
}

// lastUpdate returns the result of the last check, if any.
func (a *App) lastUpdate() (UpdateInfo, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.update == nil {
		return UpdateInfo{}, false
	}
	return *a.update, true
}
