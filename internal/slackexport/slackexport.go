// Package slackexport loads the official Slack export directory structure:
// channels.json, users.json, and per-channel/per-day message JSON files.
package slackexport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"

	"slack2notion/internal/model"
)

// Export represents a loaded Slack export directory.
type Export struct {
	Dir      string
	Users    map[string]model.SlackUser
	Channels map[string]model.SlackChannel
}

// reservedFiles are top-level JSON files in a Slack export that are not
// channel directories.
var reservedTopLevel = map[string]bool{
	"channels.json":           true,
	"users.json":              true,
	"canvases.json":           true,
	"file_conversations.json": true,
	"huddle_transcripts.json": true,
	"integration_logs.json":   true,
	"lists.json":              true,
	".slack-manifest.json":    true,
}

// Load reads channels.json and users.json from the export directory.
func Load(dir string) (*Export, error) {
	e := &Export{
		Dir:      dir,
		Users:    map[string]model.SlackUser{},
		Channels: map[string]model.SlackChannel{},
	}

	var users []model.SlackUser
	if err := readJSON(filepath.Join(dir, "users.json"), &users); err != nil {
		return nil, fmt.Errorf("loading users.json: %w", err)
	}
	for _, u := range users {
		e.Users[u.ID] = u
	}

	var channels []model.SlackChannel
	if err := readJSON(filepath.Join(dir, "channels.json"), &channels); err != nil {
		return nil, fmt.Errorf("loading channels.json: %w", err)
	}
	for _, c := range channels {
		e.Channels[c.ID] = c
	}

	return e, nil
}

// ChannelDirs returns the list of channel directory names present on disk
// (i.e. directories that are not one of the known top-level export files).
func (e *Export) ChannelDirs() ([]string, error) {
	entries, err := os.ReadDir(e.Dir)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dirs = append(dirs, entry.Name())
	}
	return dirs, nil
}

var dayFilePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.json$`)

// ChannelMessages loads and concatenates all per-day message files for the
// given channel directory name, sorted by filename (which sorts
// chronologically since files are named YYYY-MM-DD.json).
func (e *Export) ChannelMessages(channelDirName string) ([]model.RawMessage, error) {
	dir := filepath.Join(e.Dir, channelDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if dayFilePattern.MatchString(entry.Name()) {
			files = append(files, entry.Name())
		}
	}

	var all []model.RawMessage
	for _, f := range files {
		var msgs []model.RawMessage
		if err := readJSON(filepath.Join(dir, f), &msgs); err != nil {
			return nil, fmt.Errorf("loading %s: %w", f, err)
		}
		all = append(all, msgs...)
	}
	return all, nil
}

// ChannelIDByDirName finds the channel ID whose name matches the given
// export directory name (channel directories are named after the channel
// name, not its ID). Names are compared in Unicode NFC form because macOS
// returns filesystem names in NFD (decomposed) form, which would otherwise
// fail to match the NFC-encoded names stored in channels.json.
func (e *Export) ChannelIDByDirName(dirName string) (string, bool) {
	target := norm.NFC.String(dirName)
	for id, c := range e.Channels {
		if norm.NFC.String(c.Name) == target {
			return id, true
		}
	}
	return "", false
}

func readJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}
