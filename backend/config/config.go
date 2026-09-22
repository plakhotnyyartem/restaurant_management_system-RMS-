// Package config loads settings from environment variables.
//
// For local development they can live in backend/.env (see .env.example).
// Variables that are already set in the environment win over the file, so on
// a real server secrets are passed from outside and no file is needed.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Load reads the first .env it finds in the current directory, its parents
// (up to two levels) or ./backend, and exports the values that are not set yet.
// A missing file is not an error.
func Load() error {
	for _, path := range []string{".env", "backend/.env", "../.env", "../../.env"} {
		if _, err := os.Stat(path); err == nil {
			return loadFile(path)
		}
	}
	return nil
}

func loadFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=value", filepath.Base(path), n)
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

// Required returns the variable or an error that says how to fix it.
func Required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("%s is not set: copy backend/.env.example to backend/.env and fill it in", key)
	}
	return value, nil
}

// Get returns the variable or a default value.
func Get(key, def string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return def
}

var ErrWeakSecret = errors.New("JWT_SECRET must be at least 32 characters")
