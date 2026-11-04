//go:build automation && windows

package main

// UI automation for development: drive the window from scripts and tests.
// Compiled only with `-tags automation`; release builds never contain it.
//
//	go build -tags desktop,production,automation -o LanfolkDev.exe ./ui
//	LanfolkDev.exe -mock -automation 127.0.0.1:9400
//
// The server listens on loopback only and requires the token written to
// %TEMP%\lanfolk-automation.json (address and token) in an
// "Authorization: Bearer <token>" header.
//
//	POST /eval        body: JavaScript function body; "return" a JSON value.
//	                  The body runs inside an async function in the page.
//	GET  /screenshot  PNG of the window (works when it is covered).
//	POST /window?op=show|hide|minimise|move&x=..&y=..
//	GET  /tray        the tray model (icon, tooltip, menu state) as JSON.
//	POST /tray?item=open|copy-ip|settings|invite|disconnect|network&id=..

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var automationAddr = flag.String("automation", "", "development: serve the UI automation API on this loopback address")

// Dev is bound to the page only in automation builds; the page reports
// /eval results through it.
type Dev struct {
	mu      sync.Mutex
	waiting map[uint64]chan string
	next    atomic.Uint64
}

var dev = &Dev{waiting: map[uint64]chan string{}}

func devBindings() []any { return []any{dev} }

// Result is called by the page with the JSON result of an /eval.
func (d *Dev) Result(id uint64, result string) {
	d.mu.Lock()
	ch := d.waiting[id]
	delete(d.waiting, id)
	d.mu.Unlock()
	if ch != nil {
		ch <- result
	}
}

func (d *Dev) eval(ctx context.Context, wctx context.Context, body string) (string, error) {
	id := d.next.Add(1)
	ch := make(chan string, 1)
	d.mu.Lock()
	d.waiting[id] = ch
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.waiting, id); d.mu.Unlock() }()
	js := fmt.Sprintf(`(async () => {
  let out;
  try { const r = await (async () => { %s })(); out = JSON.stringify(r === undefined ? null : r); }
  catch (e) { out = JSON.stringify({ error: String(e && e.stack || e) }); }
  window.go.main.Dev.Result(%d, out);
})();`, body, id)
	wailsruntime.WindowExecJS(wctx, js)
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return "", errors.New("the page did not answer (is the window loaded?)")
	}
}

func startAutomation(host *wailsHost, tr *tray) {
	if *automationAddr == "" {
		return
	}
	ln, err := net.Listen("tcp", *automationAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "automation:", err)
		return
	}
	if ap, ok := ln.Addr().(*net.TCPAddr); !ok || !ap.IP.IsLoopback() {
		ln.Close()
		fmt.Fprintln(os.Stderr, "automation: refusing a non-loopback address")
		return
	}
	var b [16]byte
	rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	info, _ := json.Marshal(map[string]string{"addr": ln.Addr().String(), "token": token})
	os.WriteFile(filepath.Join(os.TempDir(), "lanfolk-automation.json"), info, 0o600)

	mux := http.NewServeMux()
	need := func(w http.ResponseWriter) (context.Context, bool) {
		c := host.context()
		if c == nil {
			http.Error(w, "window not ready", http.StatusServiceUnavailable)
			return nil, false
		}
		return c, true
	}
	mux.HandleFunc("POST /eval", func(w http.ResponseWriter, r *http.Request) {
		wctx, ok := need(w)
		if !ok {
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		res, err := dev.eval(ctx, wctx, string(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, res)
	})
	mux.HandleFunc("GET /screenshot", func(w http.ResponseWriter, r *http.Request) {
		png, err := captureWindow()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	})
	mux.HandleFunc("POST /window", func(w http.ResponseWriter, r *http.Request) {
		wctx, ok := need(w)
		if !ok {
			return
		}
		switch r.URL.Query().Get("op") {
		case "show":
			showWindow(wctx)
		case "hide":
			wailsruntime.WindowHide(wctx)
		case "minimise":
			wailsruntime.WindowMinimise(wctx)
		case "move":
			x, _ := strconv.Atoi(r.URL.Query().Get("x"))
			y, _ := strconv.Atoi(r.URL.Query().Get("y"))
			moveWindow(x, y)
		default:
			http.Error(w, "op must be show, hide, minimise or move", http.StatusBadRequest)
			return
		}
		io.WriteString(w, "true")
	})
	mux.HandleFunc("GET /tray", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tr.currentModel())
	})
	mux.HandleFunc("POST /tray", func(w http.ResponseWriter, r *http.Request) {
		if err := tr.simulate(r.URL.Query().Get("item"), r.URL.Query().Get("id")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		io.WriteString(w, "true")
	})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "missing or wrong token", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	fmt.Fprintln(os.Stderr, "automation: listening on", ln.Addr())
}
