package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// uiState is the little the desktop app remembers itself (all real state is
// in the service): which update it has already announced, so a notification
// does not repeat at every sign-in.
type uiState struct {
	AnnouncedUpdate string `json:"announced_update,omitempty"`
}

// uiStatePath is %APPDATA%\Lanfolk\ui-state.json.
func uiStatePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Lanfolk", "ui-state.json")
}

func loadUIState(path string) uiState {
	var s uiState
	if path == "" {
		return s
	}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func saveUIState(path string, s uiState) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
