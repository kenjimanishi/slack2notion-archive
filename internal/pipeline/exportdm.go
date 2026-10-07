// Package pipeline implements each stage of the Slack -> Notion migration:
// export-dm, normalize, classify, validate, and import-notion.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"slack2notion/internal/model"
	"slack2notion/internal/slackapi"
)

// ExportDMConfig configures the export-dm stage.
type ExportDMConfig struct {
	Token     string
	OutDir    string // e.g. data/raw/dm
	SinceDate string // YYYY-MM-DD, inclusive
	UntilDate string // YYYY-MM-DD, inclusive
}

// workspaceMeta is written to data/raw/workspace.json so later stages (which
// do not hold a Slack token) can reconstruct Slack permalinks.
type workspaceMeta struct {
	Domain string `json:"domain"`
}

// dmChannelMeta records friendly metadata about a fetched DM/MPIM
// conversation for later normalization.
type dmChannelMeta struct {
	ID           string   `json:"id"`
	DirName      string   `json:"dir_name"`
	Source       string   `json:"source"` // "dm" or "mpim"
	Participants []string `json:"participants"`
}

// unsafeNameChars matches characters unsafe for filenames. \p{M} (combining
// marks) is included alongside letters/numbers because macOS's filesystem
// returns Unicode filenames in NFD (decomposed) form, where Japanese
// dakuten/handakuten (e.g. the "゜" in "プ") are separate combining
// characters rather than part of the base letter.
var unsafeNameChars = regexp.MustCompile(`[^\p{L}\p{N}\p{M}_\-]+`)

func sanitizeDirName(name string) string {
	s := unsafeNameChars.ReplaceAllString(norm.NFC.String(name), "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "conversation"
	}
	return s
}

// RunExportDM fetches all DM and group-DM (MPIM) conversations the
// authenticated user belongs to, restricted to the [since, until] date
// range, and writes them to disk using the same per-day JSON layout as the
// official Slack export, so the normalize stage can treat both sources
// uniformly.
func RunExportDM(cfg ExportDMConfig) error {
	if cfg.Token == "" {
		return fmt.Errorf("slack user token is required (set SLACK_USER_TOKEN)")
	}
	client := slackapi.New(cfg.Token)

	domain, _, err := client.AuthTest()
	if err != nil {
		return fmt.Errorf("auth.test failed (check your token): %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.OutDir), 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(filepath.Dir(cfg.OutDir), "workspace.json"), workspaceMeta{Domain: domain}); err != nil {
		return err
	}

	since, err := parseDateBound(cfg.SinceDate, false)
	if err != nil {
		return fmt.Errorf("invalid --since date: %w", err)
	}
	until, err := parseDateBound(cfg.UntilDate, true)
	if err != nil {
		return fmt.Errorf("invalid --until date: %w", err)
	}

	convos, err := client.ListDMConversations()
	if err != nil {
		return fmt.Errorf("listing DM conversations: %w", err)
	}
	fmt.Printf("found %d DM/group-DM conversations\n", len(convos))

	userCache := map[string]string{}
	resolveUser := func(userID string) string {
		if userID == "" {
			return ""
		}
		if name, ok := userCache[userID]; ok {
			return name
		}
		info, err := client.UsersInfo(userID)
		name := userID
		if err == nil && info != nil {
			if info.Profile.DisplayName != "" {
				name = info.Profile.DisplayName
			} else if info.Profile.RealName != "" {
				name = info.Profile.RealName
			} else if info.RealName != "" {
				name = info.RealName
			} else if info.Name != "" {
				name = info.Name
			}
		}
		userCache[userID] = name
		return name
	}

	var channelMetas []dmChannelMeta

	for _, convo := range convos {
		var dirName, source string
		participantSet := map[string]bool{}

		if convo.IsIM {
			other := resolveUser(convo.User)
			dirName = "DM_" + sanitizeDirName(other)
			source = string(model.SourceDM)
			participantSet[other] = true
		} else if convo.IsMpim {
			dirName = "Group_" + sanitizeDirName(convo.Name)
			source = string(model.SourceMPIM)
		} else {
			continue
		}

		fmt.Printf("fetching history for %s (%s)...\n", dirName, convo.ID)
		history, err := client.History(convo.ID, strconv.FormatInt(since, 10), strconv.FormatInt(until, 10))
		if err != nil {
			fmt.Printf("  warning: failed to fetch history for %s: %v\n", convo.ID, err)
			continue
		}

		// Fetch full thread replies for any message that is a thread root
		// with replies, since conversations.history only returns the root.
		merged := map[string]slackapi.RawHistoryMessage{}
		for _, m := range history {
			merged[m.Ts] = m
		}
		for _, m := range history {
			if m.ReplyCount > 0 && (m.ThreadTs == "" || m.ThreadTs == m.Ts) {
				replies, err := client.Replies(convo.ID, m.Ts)
				if err != nil {
					fmt.Printf("  warning: failed to fetch replies for thread %s: %v\n", m.Ts, err)
					continue
				}
				for _, r := range replies {
					merged[r.Ts] = r
				}
			}
		}

		var allMsgs []model.RawMessage
		for _, m := range merged {
			raw := model.RawMessage{
				Type:       m.Type,
				Subtype:    m.Subtype,
				User:       m.User,
				Text:       m.Text,
				Ts:         m.Ts,
				ThreadTs:   m.ThreadTs,
				ReplyCount: m.ReplyCount,
			}
			for _, f := range m.Files {
				raw.Files = append(raw.Files, model.SlackFile{
					ID:                 f.ID,
					Name:               f.Name,
					Title:              f.Title,
					Mimetype:           f.Mimetype,
					Filetype:           f.Filetype,
					Size:               f.Size,
					URLPrivate:         f.URLPrivate,
					URLPrivateDownload: f.URLPrivateDownload,
					Permalink:          f.Permalink,
				})
			}
			allMsgs = append(allMsgs, raw)
			if m.User != "" {
				participantSet[resolveUser(m.User)] = true
			}
		}
		var participants []string
		for p := range participantSet {
			participants = append(participants, p)
		}
		sort.Strings(participants)

		if err := writeMessagesByDay(filepath.Join(cfg.OutDir, dirName), allMsgs); err != nil {
			return fmt.Errorf("writing messages for %s: %w", dirName, err)
		}

		channelMetas = append(channelMetas, dmChannelMeta{
			ID:           convo.ID,
			DirName:      dirName,
			Source:       source,
			Participants: participants,
		})
		fmt.Printf("  wrote %d messages\n", len(allMsgs))
		time.Sleep(200 * time.Millisecond)
	}

	if err := writeJSON(filepath.Join(cfg.OutDir, "dm_channels.json"), channelMetas); err != nil {
		return err
	}

	fmt.Printf("export-dm complete: %d conversations written to %s\n", len(channelMetas), cfg.OutDir)
	return nil
}

// parseDateBound converts a YYYY-MM-DD string into a Unix timestamp bound.
// An empty date means "unbounded": the earliest possible time (epoch) when
// used as a lower bound, or the current time when used as an upper bound.
func parseDateBound(date string, endOfDay bool) (int64, error) {
	if date == "" {
		if endOfDay {
			return time.Now().Unix(), nil
		}
		return 0, nil
	}
	t, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return 0, err
	}
	if endOfDay {
		t = t.Add(24*time.Hour - time.Second)
	}
	return t.Unix(), nil
}

// writeMessagesByDay groups raw messages by their date (derived from the ts
// field) and writes one JSON array file per day, matching the official
// Slack export layout.
func writeMessagesByDay(dir string, msgs []model.RawMessage) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	byDay := map[string][]model.RawMessage{}
	for _, m := range msgs {
		day := tsToDay(m.Ts)
		byDay[day] = append(byDay[day], m)
	}
	for day, dayMsgs := range byDay {
		sort.Slice(dayMsgs, func(i, j int) bool { return dayMsgs[i].Ts < dayMsgs[j].Ts })
		if err := writeJSON(filepath.Join(dir, day+".json"), dayMsgs); err != nil {
			return err
		}
	}
	return nil
}

func tsToDay(ts string) string {
	sec, _, _ := strings.Cut(ts, ".")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return "unknown"
	}
	return time.Unix(n, 0).Local().Format("2006-01-02")
}

func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
