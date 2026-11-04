package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NotrinoResearch/lanfolk/internal/ipc"
	"github.com/NotrinoResearch/lanfolk/internal/version"
)

func TestSemverCompare(t *testing.T) {
	order := []string{
		"0.2.0-alpha", "0.2.0-alpha.1", "0.2.0-alpha.beta", "0.2.0-beta", "0.2.0-beta.2",
		"0.2.0-beta.11", "0.2.0-rc.1", "0.2.0", "v0.2.1", "0.3.0", "0.10.0", "1.0.0",
	}
	for i := range order {
		for j := range order {
			a, ok1 := parseSemver(order[i])
			b, ok2 := parseSemver(order[j])
			if !ok1 || !ok2 {
				t.Fatalf("parse %q %q", order[i], order[j])
			}
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.compare(b); got != want {
				t.Errorf("%s vs %s = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	for _, bad := range []string{"", "1.2", "1.2.3.4", "a.b.c", "1.2.3-", "-1.0.0", "1..2"} {
		if _, ok := parseSemver(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if v, ok := parseSemver("1.2.3+build.5"); !ok || v.n != [3]int{1, 2, 3} {
		t.Errorf("build metadata: %+v %v", v, ok)
	}
}

func TestNewestRelease(t *testing.T) {
	rels := []ghRelease{
		{TagName: "v0.2.0", Prerelease: true},
		{TagName: "v0.4.0", Draft: true},
		{TagName: "v0.3.0-rc.1", Prerelease: true},
		{TagName: "not-a-version"},
		{TagName: "v0.2.1"},
	}
	cur, _ := parseSemver("0.2.0")
	if r, _, ok := newestRelease(rels, cur); !ok || r.TagName != "v0.3.0-rc.1" {
		t.Fatalf("0.x build: got %+v", r)
	}
	// From 1.0 on, stable builds ignore pre-releases.
	cur, _ = parseSemver("1.0.0")
	rels = append(rels, ghRelease{TagName: "v1.0.1"}, ghRelease{TagName: "v1.1.0-beta", Prerelease: true})
	if r, _, ok := newestRelease(rels, cur); !ok || r.TagName != "v1.0.1" {
		t.Fatalf("1.x build: got %+v", r)
	}
}

func releasesServer(t *testing.T, status int, body string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "Lanfolk/") || strings.Contains(ua, "@") {
			t.Errorf("User-Agent %q", ua)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckForUpdate(t *testing.T) {
	ctx := context.Background()
	srv := releasesServer(t, 200, `[
		{"tag_name":"v0.3.0","html_url":"https://github.com/NotrinoResearch/lanfolk/releases/tag/v0.3.0","prerelease":true},
		{"tag_name":"v0.2.0","html_url":"https://github.com/NotrinoResearch/lanfolk/releases/tag/v0.2.0","prerelease":true}]`)
	info := checkForUpdate(ctx, srv.Client(), srv.URL, "0.2.0")
	if !info.Available || info.Latest != "0.3.0" || info.Error != "" ||
		info.URL != "https://github.com/NotrinoResearch/lanfolk/releases/tag/v0.3.0" {
		t.Fatalf("%+v", info)
	}
	if info := checkForUpdate(ctx, srv.Client(), srv.URL, "0.3.0"); info.Available || info.Latest != "0.3.0" {
		t.Fatalf("up to date: %+v", info)
	}
	if info := checkForUpdate(ctx, srv.Client(), srv.URL, "0.4.0"); info.Available {
		t.Fatalf("newer than the newest release: %+v", info)
	}

	// Only GitHub pages are opened.
	evil := releasesServer(t, 200, `[{"tag_name":"v9.0.0","html_url":"https://evil.example/x"}]`)
	if info := checkForUpdate(ctx, evil.Client(), evil.URL, "0.2.0"); info.URL != releasesPage {
		t.Fatalf("url %q", info.URL)
	}

	for _, c := range []struct {
		status int
		body   string
	}{{403, `{"message":"rate limited"}`}, {200, `<html>`}, {200, `[]`}} {
		srv := releasesServer(t, c.status, c.body)
		info := checkForUpdate(ctx, srv.Client(), srv.URL, "0.2.0")
		if info.Available || (c.body != `[]` && info.Error == "") {
			t.Errorf("%d %s: %+v", c.status, c.body, info)
		}
	}
	if info := checkForUpdate(ctx, http.DefaultClient, "http://127.0.0.1:1", "0.2.0"); info.Error == "" {
		t.Fatal("unreachable server reported no error")
	}
	if info := checkForUpdate(ctx, http.DefaultClient, srv.URL, "0.0.0-dev+x"); info.Error != "" && info.Available {
		t.Fatalf("dev build: %+v", info)
	}
}

func TestCheckForUpdatesEmitsAndResyncRepeats(t *testing.T) {
	old := version.Version
	version.Version = "0.2.0"
	defer func() { version.Version = old }()
	srv := releasesServer(t, 200, `[{"tag_name":"v0.3.0","html_url":"https://github.com/NotrinoResearch/lanfolk/releases/tag/v0.3.0"}]`)
	h := &fakeHost{}
	a := NewApp("unused", h)
	a.httpClient, a.updateURL = srv.Client(), srv.URL
	info, err := a.CheckForUpdates()
	if err != nil || !info.Available {
		t.Fatalf("%+v %v", info, err)
	}
	h.wait(t, eventUpdate, func(d any) bool { return d.(UpdateInfo).Latest == "0.3.0" })

	// A later failure keeps the known release.
	a.updateURL = "http://127.0.0.1:1"
	info, err = a.CheckForUpdates()
	if err != nil || info.Latest != "0.3.0" || info.Error == "" {
		t.Fatalf("after failure: %+v %v", info, err)
	}

	h.clear()
	a.announced = true
	a.Resync()
	h.wait(t, eventUpdate, func(d any) bool { return d.(UpdateInfo).Latest == "0.3.0" })
}

// The loop asks the service for the setting and skips the check while it
// is off or unknown.
func TestUpdateLoopRespectsSetting(t *testing.T) {
	old := version.Version
	version.Version = "0.2.0"
	defer func() { version.Version = old }()
	hits := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	a := NewApp("unused", &fakeHost{})
	a.httpClient, a.updateURL, a.updateFirst = srv.Client(), srv.URL, 10*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.updateLoop(ctx) // no service connection: setting unknown
	select {
	case <-hits:
		t.Fatal("checked without knowing the setting")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestUpdateLoopChecksWhenOn(t *testing.T) {
	old := version.Version
	version.Version = "0.2.0"
	defer func() { version.Version = old }()
	for _, on := range []bool{true, false} {
		hits := make(chan struct{}, 10)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits <- struct{}{}
			fmt.Fprint(w, `[]`)
		}))
		f := newFake(t, testAddr(t), map[string]any{ipc.MethodGetSettings: ipc.Settings{CheckUpdates: on}})
		h := &fakeHost{}
		a := fastApp(f.addr, h)
		a.mode = "main"
		a.httpClient, a.updateURL, a.updateFirst = srv.Client(), srv.URL, 100*time.Millisecond
		a.start(context.Background())
		h.wait(t, "service", serviceIs(true))
		select {
		case <-hits:
			if !on {
				t.Error("checked although the setting is off")
			}
		case <-time.After(time.Second):
			if on {
				t.Error("no check although the setting is on")
			}
		}
		a.stop()
		srv.Close()
	}
}
