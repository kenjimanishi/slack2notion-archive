package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"slack2notion/internal/fileassets"
	"slack2notion/internal/model"
)

// FetchFilesConfig configures the file-download stage.
type FetchFilesConfig struct {
	ClassifiedDir string // data/classified (updated in place)
	FilesDir      string // data/files
	Budget        fileassets.Budget
	// SlackToken is sent as a Bearer Authorization header when downloading.
	// Required for files obtained via the Slack Web API (e.g. DM
	// attachments); files from the official export work with or without it
	// since their URLs carry a pre-signed token already.
	SlackToken string
}

// RunFetchFiles walks every classified thread, and for each attached file
// that fits within the configured budget, downloads it to local disk and
// records the local path. Files beyond budget keep only their Slack link
// (url_private_download / permalink), which may expire over time.
func RunFetchFiles(cfg FetchFilesConfig) error {
	entries, err := os.ReadDir(cfg.ClassifiedDir)
	if err != nil {
		return fmt.Errorf("reading classified dir: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(cfg.ClassifiedDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var threads []model.ClassifiedThread
		if err := json.Unmarshal(data, &threads); err != nil {
			return fmt.Errorf("parsing %s: %w", e.Name(), err)
		}

		downloader := fileassets.NewDownloader(cfg.FilesDir, cfg.Budget, cfg.SlackToken)

		// Build a flat, chronologically sorted list of file pointers so we
		// prioritize older content when the budget runs out.
		type ptr struct{ ti, mi, fi int }
		var ptrs []ptr
		for ti, t := range threads {
			for mi, m := range t.Messages {
				for fi := range m.Files {
					ptrs = append(ptrs, ptr{ti, mi, fi})
				}
			}
		}
		sort.Slice(ptrs, func(i, j int) bool {
			a := threads[ptrs[i].ti].Messages[ptrs[i].mi].Time
			b := threads[ptrs[j].ti].Messages[ptrs[j].mi].Time
			return a.Before(b)
		})

		changed := false
		for _, p := range ptrs {
			f := &threads[p.ti].Messages[p.mi].Files[p.fi]
			if !downloader.ShouldDownload(f.Size) {
				continue
			}
			url := f.URLPrivateDownload
			if url == "" {
				continue
			}
			localPath, err := downloader.Download(threads[p.ti].MigrationID, f.ID, f.Name, url, f.Size)
			if err != nil {
				fmt.Printf("  warning: failed to download %s: %v\n", f.Name, err)
				continue
			}
			f.LocalPath = localPath
			f.Downloaded = true
			changed = true
			time.Sleep(100 * time.Millisecond)
		}

		if changed {
			if err := writeJSON(path, threads); err != nil {
				return err
			}
		}
	}

	fmt.Println("fetch-files complete")
	return nil
}
