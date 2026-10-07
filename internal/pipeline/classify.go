package pipeline

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"slack2notion/internal/model"
)

// ClassifyConfig configures the classify stage.
type ClassifyConfig struct {
	NormalizedDir string // data/normalized
	OutDir        string // data/classified
}

// Tag constants used for the ContentType/Tag classification. Kept as plain
// strings (not an enum) since they map directly to Notion multi-select
// option names.
const (
	TagText     = "テキスト"
	TagImage    = "画像"
	TagVideo    = "動画"
	TagFile     = "ファイル"
	TagLink     = "リンク共有"
	TagReminder = "リマインダー"
	TagSystem   = "システム"
	TagOther    = "その他"
)

var (
	reminderRe = regexp.MustCompile(`リマインダー|リマインド|にこのチャンネルでこれをリマインド`)
	urlOnlyRe  = regexp.MustCompile(`^\s*(https?://\S+)(\s+\(https?://\S+\))?\s*$`)
)

// RunClassify reads every normalized thread file, assigns a channel-based
// Category plus rule-based content Tags, computes a stable MigrationID, and
// writes the result to the classified directory.
func RunClassify(cfg ClassifyConfig) error {
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(cfg.NormalizedDir)
	if err != nil {
		return fmt.Errorf("reading normalized dir: %w", err)
	}

	total := 0
	tagCounts := map[string]int{}

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cfg.NormalizedDir, e.Name()))
		if err != nil {
			return err
		}
		var threads []model.NormalizedThread
		if err := json.Unmarshal(data, &threads); err != nil {
			return fmt.Errorf("parsing %s: %w", e.Name(), err)
		}

		var classified []model.ClassifiedThread
		for _, t := range threads {
			ct := classifyThread(t)
			classified = append(classified, ct)
			for _, tag := range ct.Tags {
				tagCounts[tag]++
			}
		}

		if err := writeJSON(filepath.Join(cfg.OutDir, e.Name()), classified); err != nil {
			return err
		}
		total += len(classified)
	}

	fmt.Printf("classify complete: %d threads classified\n", total)
	fmt.Println("tag distribution:")
	for tag, count := range tagCounts {
		fmt.Printf("  %-10s %d\n", tag, count)
	}
	return nil
}

func classifyThread(t model.NormalizedThread) model.ClassifiedThread {
	tagSet := map[string]bool{}
	hasFile := false
	hasText := false
	allSystem := true

	for _, m := range t.Messages {
		if !m.IsSystem {
			allSystem = false
		}
		trimmed := strings.TrimSpace(m.Text)

		if reminderRe.MatchString(m.Text) {
			tagSet[TagReminder] = true
		}
		if m.IsSystem && trimmed != "" {
			tagSet[TagSystem] = true
		}

		for _, f := range m.Files {
			hasFile = true
			switch {
			case strings.HasPrefix(f.Mimetype, "image/"):
				tagSet[TagImage] = true
			case strings.HasPrefix(f.Mimetype, "video/"):
				tagSet[TagVideo] = true
			default:
				tagSet[TagFile] = true
			}
		}

		if trimmed != "" && !m.IsSystem {
			if urlOnlyRe.MatchString(trimmed) {
				tagSet[TagLink] = true
			} else {
				hasText = true
			}
		}
	}

	if hasText {
		tagSet[TagText] = true
	}
	if allSystem && len(t.Messages) > 0 {
		tagSet[TagSystem] = true
	}
	if len(tagSet) == 0 {
		tagSet[TagOther] = true
	}

	var tags []string
	for tag := range tagSet {
		tags = append(tags, tag)
	}

	return model.ClassifiedThread{
		NormalizedThread: t,
		MigrationID:      migrationID(t.ChannelID, t.ChannelName, t.ThreadTs),
		Category:         t.ChannelName,
		Tags:             tags,
		HasFile:          hasFile,
	}
}

// migrationID produces a short, stable, collision-resistant key used as the
// idempotency key in Notion. It falls back to hashing the channel name when
// no channel ID is available (e.g. some DM sources).
func migrationID(channelID, channelName, threadTs string) string {
	key := channelID
	if key == "" {
		key = channelName
	}
	sum := sha1.Sum([]byte(key + ":" + threadTs))
	return fmt.Sprintf("%x", sum)[:20]
}
