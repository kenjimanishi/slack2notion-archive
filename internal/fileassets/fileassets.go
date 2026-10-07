// Package fileassets downloads Slack file attachments to local disk so they
// can be re-uploaded to Notion, subject to a size/count budget. Files that
// exceed the budget are left as Slack links (which may expire over time).
package fileassets

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Budget controls how many bytes of file content will be downloaded in
// total, and the maximum size of any single file considered for download.
type Budget struct {
	MaxSingleFileBytes int64
	MaxTotalBytes      int64
}

// DefaultBudget is a conservative default. The per-file cap is kept just
// under Notion's 20MiB single-request Direct Upload limit, since downloaded
// files are re-uploaded to Notion via that API; anything larger falls back
// to the original (expiring) Slack link. The total cap avoids an
// unbounded, long-running first migration.
var DefaultBudget = Budget{
	MaxSingleFileBytes: 19 * 1024 * 1024,
	MaxTotalBytes:      300 * 1024 * 1024,
}

// unsafeChars matches characters unsafe for filenames. \p{M} is included
// for the same NFD-decomposed-Unicode reason documented in
// pipeline.unsafeNameChars.
var unsafeChars = regexp.MustCompile(`[^\p{L}\p{N}\p{M}_\-.]+`)

func SanitizeFileName(name string) string {
	s := unsafeChars.ReplaceAllString(name, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "file"
	}
	return s
}

// Downloader tracks cumulative downloaded bytes against a Budget across
// many Download calls.
type Downloader struct {
	Budget Budget
	OutDir string
	// Token, if set, is sent as a Bearer Authorization header on every
	// download request. Files fetched via the Slack Web API (e.g. DM
	// attachments) require this; files from the official compliance
	// export already carry a pre-signed token in their URL and work
	// without it, so sending it in both cases is safe.
	Token      string
	totalBytes int64
	client     *http.Client
}

// NewDownloader creates a Downloader that writes files under outDir.
func NewDownloader(outDir string, budget Budget, token string) *Downloader {
	return &Downloader{
		Budget: budget,
		OutDir: outDir,
		Token:  token,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// ShouldDownload reports whether a file of the given size can still be
// downloaded under the remaining budget.
func (d *Downloader) ShouldDownload(size int64) bool {
	if size <= 0 {
		return false
	}
	if size > d.Budget.MaxSingleFileBytes {
		return false
	}
	return d.totalBytes+size <= d.Budget.MaxTotalBytes
}

// Download fetches the file at url and saves it under
// <OutDir>/<migrationID>/<fileID>_<fileName>. It returns the local path
// (relative to OutDir's parent) on success.
func (d *Downloader) Download(migrationID, fileID, fileName, url string, size int64) (string, error) {
	dir := filepath.Join(d.OutDir, SanitizeFileName(migrationID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	localName := SanitizeFileName(fileID) + "_" + SanitizeFileName(fileName)
	localPath := filepath.Join(dir, localName)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if d.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.Token)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d downloading %s", resp.StatusCode, fileID)
	}

	out, err := os.Create(localPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		return "", err
	}
	d.totalBytes += written
	return localPath, nil
}
