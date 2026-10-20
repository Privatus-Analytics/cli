package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultEndpoint is the hosted Privatus Analytics app.
const DefaultEndpoint = "https://privatusanalytics.com"

// Config is persisted in <user config dir>/privatus/config.json (0600).
type Config struct {
	Token     string `json:"token,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// configPath returns the config file location. PRIVATUS_CONFIG_DIR
// overrides the directory (used by tests and CI).
func configPath(env func(string) string) (string, error) {
	dir := env("PRIVATUS_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("finding the user config directory: %w", err)
		}
		dir = filepath.Join(base, "privatus")
	}
	return filepath.Join(dir, "config.json"), nil
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &c, nil
}

// save writes the config atomically with 0600 permissions in a 0700 dir.
func (c *Config) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
