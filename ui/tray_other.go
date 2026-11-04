//go:build !windows

package main

// The tray is Windows-only; elsewhere these are no-ops so the package builds.
type tray struct{}

func newTray(*App, *wailsHost) *tray          { return &tray{} }
func (t *tray) start()                        {}
func (t *tray) stop()                         {}
func (t *tray) observe(string, any)           {}
func (t *tray) currentModel() trayModel       { return trayModel{} }
func (t *tray) simulate(string, string) error { return nil }
