package main

import (
	"context"
	"sync"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Host is everything the App needs from the window system. Keeping it behind
// an interface lets the tests run without a window.
type Host interface {
	// Emit sends an event to the frontend.
	Emit(event string, data any)
	// SaveFile asks the user where to save a file. It returns "" when the
	// user cancels.
	SaveFile(title, defaultName string) (string, error)
	// OpenFile asks the user to pick a file. It returns "" when the user
	// cancels.
	OpenFile(title string) (string, error)
	// SetClipboard puts text on the system clipboard.
	SetClipboard(text string) error
}

// wailsHost implements Host with the Wails runtime. It becomes usable once
// the application has started and Wails has handed over its context.
type wailsHost struct {
	mu  sync.Mutex
	ctx context.Context
}

func (h *wailsHost) setContext(ctx context.Context) {
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
}

func (h *wailsHost) context() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ctx
}

func (h *wailsHost) Emit(event string, data any) {
	if ctx := h.context(); ctx != nil {
		wailsruntime.EventsEmit(ctx, event, data)
	}
}

func (h *wailsHost) SaveFile(title, defaultName string) (string, error) {
	ctx := h.context()
	if ctx == nil {
		return "", errWindowNotReady
	}
	return wailsruntime.SaveFileDialog(ctx, wailsruntime.SaveDialogOptions{
		Title:                title,
		DefaultFilename:      defaultName,
		CanCreateDirectories: true,
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Text files (*.txt)", Pattern: "*.txt"},
			{DisplayName: "All files (*.*)", Pattern: "*.*"},
		},
	})
}

func (h *wailsHost) OpenFile(title string) (string, error) {
	ctx := h.context()
	if ctx == nil {
		return "", errWindowNotReady
	}
	return wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
		Title: title,
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Text files (*.txt)", Pattern: "*.txt"},
			{DisplayName: "All files (*.*)", Pattern: "*.*"},
		},
	})
}

func (h *wailsHost) SetClipboard(text string) error {
	ctx := h.context()
	if ctx == nil {
		return errWindowNotReady
	}
	return wailsruntime.ClipboardSetText(ctx, text)
}
