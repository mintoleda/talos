package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Discovery is the contents of ~/.talos/daemon.json — written by the
// multi-session daemon on startup so clients can find and authenticate.
type Discovery struct {
	PID       int       `json:"pid"`
	Socket    string    `json:"socket"`
	WS        string    `json:"ws"`
	Token     string    `json:"token"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}

// DiscoveryPath returns the default path for the daemon discovery file.
func DiscoveryPath(baseDir string) string {
	return filepath.Join(baseDir, "daemon.json")
}

// WriteDiscovery atomically writes discovery to path with mode 0600.
func WriteDiscovery(path string, d Discovery) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("discovery mkdir: %w", err)
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("discovery write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("discovery rename: %w", err)
	}
	return nil
}

// ReadDiscovery loads and validates the discovery file. It returns an error
// if the file is missing, malformed, or stale (socket not accepting connections).
func ReadDiscovery(path string) (Discovery, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Discovery{}, err
	}
	var d Discovery
	if err := json.Unmarshal(data, &d); err != nil {
		return Discovery{}, fmt.Errorf("discovery parse: %w", err)
	}
	if d.Socket == "" {
		return Discovery{}, fmt.Errorf("discovery: missing socket")
	}
	if !IsAlive(d.Socket) {
		return Discovery{}, fmt.Errorf("discovery: stale (socket %s not alive)", d.Socket)
	}
	return d, nil
}

// RemoveDiscovery deletes the discovery file. Missing file is not an error.
func RemoveDiscovery(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
