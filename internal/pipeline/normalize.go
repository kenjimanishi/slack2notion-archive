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

	"slack2notion/internal/model"
	"slack2notion/internal/slackexport"
)

// NormalizeConfig configures the normalize stage.
type NormalizeConfig struct {
	ExportDir string // official Slack export dir (public channels)
	DMRawDir  string // data/raw/dm
	OutDir    string // data/normalized
}

// RunNormalize loads the official public-channel export and the DM export
// produced by RunExportDM, resolves user/channel names, converts Slack
// mrkdwn markup to plain readable text, reconstructs threads, and writes one
// JSON file per channel/conversation containing a list of NormalizedThread.
func RunNormalize(cfg NormalizeConfig) error {
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}

	exp, err := slackexport.Load(cfg.ExportDir)
	if err != nil {
		return fmt.Errorf("loading slack export: %w", err)
	}

	domain := loadWorkspaceDomain(cfg.DMRawDir)

	users := map[string]model.SlackUser{}
	for id, u := range exp.Users {
		users[id] = u
	}

	resolveUser := func(id string) string {
		if id == "" {
			return ""
		}
		if u, ok := users[id]; ok {
			return u.DisplayName()
		}
		return id
	}
	resolveChannelMention := func(id string) string {
		if c, ok := exp.Channels[id]; ok {
			return c.Name
		}
		return id
	}

	total := 0

	// --- Public channels ---
	dirs, err := exp.ChannelDirs()
	if err != nil {
		return fmt.Errorf("listing channel dirs: %w", err)
	}
	for _, dirName := range dirs {
		channelID, _ := exp.ChannelIDByDirName(dirName)
		rawMsgs, err := exp.ChannelMessages(dirName)
		if err != nil {
			return fmt.Errorf("loading messages for channel %s: %w", dirName, err)
		}
		threads := buildThreads(rawMsgs, channelID, dirName, model.SourcePublicChannel, domain, resolveUser, resolveChannelMention)
		if err := writeJSON(filepath.Join(cfg.OutDir, sanitizeDirName(dirName)+".json"), threads); err != nil {
			return err
		}
		total += len(threads)
		fmt.Printf("normalized channel %-30s %4d messages -> %4d threads\n", dirName, len(rawMsgs), len(threads))
	}

	// --- DMs / group DMs ---
	dmMetas, dmErr := loadDMChannelMetas(cfg.DMRawDir)
	if dmErr == nil {
		for _, meta := range dmMetas {
			rawMsgs, err := readRawMessagesDir(filepath.Join(cfg.DMRawDir, meta.DirName))
			if err != nil {
				fmt.Printf("  warning: could not read DM dir %s: %v\n", meta.DirName, err)
				continue
			}
			channelName := "DM: " + strings.Join(meta.Participants, ", ")
			if meta.Source == string(model.SourceMPIM) {
				channelName = "Group DM: " + strings.Join(meta.Participants, ", ")
			}
			source := model.Source(meta.Source)
			threads := buildThreads(rawMsgs, meta.ID, channelName, source, domain, resolveUser, resolveChannelMention)
			if err := writeJSON(filepath.Join(cfg.OutDir, sanitizeDirName(meta.DirName)+".json"), threads); err != nil {
				return err
			}
			total += len(threads)
			fmt.Printf("normalized %-30s %4d messages -> %4d threads\n", meta.DirName, len(rawMsgs), len(threads))
		}
	} else {
		fmt.Printf("note: no DM export data found (%v); run export-dm first if DMs are needed\n", dmErr)
	}

	fmt.Printf("normalize complete: %d threads total\n", total)
	return nil
}

func loadWorkspaceDomain(dmRawDir string) string {
	if env := os.Getenv("SLACK_WORKSPACE_DOMAIN"); env != "" {
		return env
	}
	var meta workspaceMeta
	path := filepath.Join(filepath.Dir(dmRawDir), "workspace.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return meta.Domain
}

func loadDMChannelMetas(dmRawDir string) ([]dmChannelMeta, error) {
	var metas []dmChannelMeta
	data, err := os.ReadFile(filepath.Join(dmRawDir, "dm_channels.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &metas); err != nil {
		return nil, err
	}
	return metas, nil
}

func readRawMessagesDir(dir string) ([]model.RawMessage, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var all []model.RawMessage
	dayFile := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.json$`)
	for _, e := range entries {
		if e.IsDir() || !dayFile.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var msgs []model.RawMessage
		if err := json.Unmarshal(data, &msgs); err != nil {
			return nil, err
		}
		all = append(all, msgs...)
	}
	return all, nil
}

// --- mrkdwn conversion ---

var (
	userMentionRe    = regexp.MustCompile(`<@([UW][A-Z0-9]+)>`)
	channelMentionRe = regexp.MustCompile(`<#([C][A-Z0-9]+)(\|[^>]*)?>`)
	specialMentionRe = regexp.MustCompile(`<!(here|channel|everyone)(\^[^>]*)?>`)
	linkRe           = regexp.MustCompile(`<(https?://[^|>]+)(\|([^>]*))?>`)
)

// convertMrkdwn resolves Slack's mrkdwn mention/link syntax into
// plain, human-readable text suitable for display in Notion.
func convertMrkdwn(text string, resolveUser func(string) string, resolveChannel func(string) string) string {
	text = userMentionRe.ReplaceAllStringFunc(text, func(m string) string {
		id := userMentionRe.FindStringSubmatch(m)[1]
		return "@" + resolveUser(id)
	})
	text = channelMentionRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := channelMentionRe.FindStringSubmatch(m)
		id := sub[1]
		if sub[2] != "" {
			return "#" + strings.TrimPrefix(sub[2], "|")
		}
		return "#" + resolveChannel(id)
	})
	text = specialMentionRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := specialMentionRe.FindStringSubmatch(m)
		return "@" + sub[1]
	})
	text = linkRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := linkRe.FindStringSubmatch(m)
		url, label := sub[1], sub[3]
		if label == "" {
			label = url
		}
		return "[" + label + "](" + url + ")"
	})
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	return text
}

// --- thread reconstruction ---

func buildThreads(
	raw []model.RawMessage,
	channelID, channelName string,
	source model.Source,
	domain string,
	resolveUser func(string) string,
	resolveChannel func(string) string,
) []model.NormalizedThread {
	groups := map[string][]model.RawMessage{}
	var order []string
	for _, m := range raw {
		key := m.ThreadTs
		if key == "" {
			key = m.Ts
		}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], m)
	}
	sort.Strings(order)

	var threads []model.NormalizedThread
	for _, key := range order {
		msgs := groups[key]
		sort.Slice(msgs, func(i, j int) bool { return msgs[i].Ts < msgs[j].Ts })

		var normMsgs []model.NormalizedMessage
		participantSet := map[string]bool{}
		for _, m := range msgs {
			author := resolveUser(m.User)
			if author == "" && m.Username != "" {
				author = m.Username
			}
			if author != "" {
				participantSet[author] = true
			}
			var files []model.NormalizedFile
			for _, f := range m.Files {
				files = append(files, model.NormalizedFile{
					ID:                 f.ID,
					Name:               nonEmpty(f.Title, f.Name),
					Mimetype:           f.Mimetype,
					Filetype:           f.Filetype,
					Size:               f.Size,
					URLPrivateDownload: f.URLPrivateDownload,
					Permalink:          f.Permalink,
				})
			}
			normMsgs = append(normMsgs, model.NormalizedMessage{
				AuthorID: m.User,
				Author:   author,
				Ts:       m.Ts,
				Time:     tsToTime(m.Ts),
				Text:     convertMrkdwn(m.Text, resolveUser, resolveChannel),
				Files:    files,
				IsSystem: m.Subtype != "" && m.Subtype != "bot_message",
			})
		}

		var participants []string
		for p := range participantSet {
			participants = append(participants, p)
		}
		sort.Strings(participants)

		var permalink string
		if domain != "" && channelID != "" {
			permalink = fmt.Sprintf("https://%s/archives/%s/p%s", domain, channelID, tsToPermalinkFragment(key))
		}

		threads = append(threads, model.NormalizedThread{
			ChannelID:    channelID,
			ChannelName:  channelName,
			Source:       source,
			ThreadTs:     key,
			Messages:     normMsgs,
			Participants: participants,
			StartedAt:    tsToTime(msgs[0].Ts),
			LastUpdated:  tsToTime(msgs[len(msgs)-1].Ts),
			Permalink:    permalink,
		})
	}
	return threads
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func tsToTime(ts string) time.Time {
	sec, _, _ := strings.Cut(ts, ".")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// tsToPermalinkFragment converts a Slack ts ("1693185679.672649") into the
// zero-padded, dot-free fragment Slack uses in permalink URLs.
func tsToPermalinkFragment(ts string) string {
	return strings.Replace(ts, ".", "", 1)
}
