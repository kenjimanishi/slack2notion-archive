package main

import (
	"bufio"
	"os"
	"strings"
)

// loadDotEnv reads a simple KEY=VALUE .env file (if present) and sets any
// variables not already present in the process environment. Existing
// environment variables always take precedence, so CI/shell exports still
// override .env. Lines starting with # are comments; surrounding single or
// double quotes around the value are stripped.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // no .env file: silently proceed with the real environment
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
}
