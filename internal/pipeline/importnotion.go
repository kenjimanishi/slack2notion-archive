package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"slack2notion/internal/model"
	"slack2notion/internal/notionapi"
)

// ImportNotionConfig configures the import-notion stage.
type ImportNotionConfig struct {
	ClassifiedDir string
	DatabaseID    string
	Client        *notionapi.Client
	Pilot         int // if > 0, only import this many threads (for a trial run)
}

// RunImportNotion imports classified threads into the Notion "Slack
// Archive" database, one thread per page. Each page's Migration ID
// property is used to detect threads already imported in a previous run,
// so this stage can be re-run safely (existing pages are skipped).
func RunImportNotion(cfg ImportNotionConfig) error {
	threads, err := loadAllClassified(cfg.ClassifiedDir)
	if err != nil {
		return err
	}

	// Import oldest-first so a pilot run (which stops early) produces a
	// representative, chronologically coherent sample.
	sort.Slice(threads, func(i, j int) bool { return threads[i].StartedAt.Before(threads[j].StartedAt) })

	if cfg.Pilot > 0 && cfg.Pilot < len(threads) {
		fmt.Printf("pilot mode: importing %d of %d threads\n", cfg.Pilot, len(threads))
		threads = threads[:cfg.Pilot]
	}

	created, skipped, failed := 0, 0, 0
	for i, t := range threads {
		existing, err := cfg.Client.QueryDatabaseByRichText(cfg.DatabaseID, "Migration ID", t.MigrationID)
		if err != nil {
			fmt.Printf("  [%d/%d] warning: could not check for existing page (%s): %v\n", i+1, len(threads), t.MigrationID, err)
		}
		if existing != "" {
			skipped++
			continue
		}

		if err := importThread(cfg.Client, cfg.DatabaseID, t); err != nil {
			fmt.Printf("  [%d/%d] failed to import thread %s: %v\n", i+1, len(threads), t.MigrationID, err)
			failed++
			continue
		}
		created++
		if (i+1)%10 == 0 || i == len(threads)-1 {
			fmt.Printf("  [%d/%d] imported (created=%d skipped=%d failed=%d)\n", i+1, len(threads), created, skipped, failed)
		}
		time.Sleep(350 * time.Millisecond) // stay comfortably under Notion's ~3 req/s limit
	}

	fmt.Printf("import-notion complete: created=%d skipped(existing)=%d failed=%d\n", created, skipped, failed)
	return nil
}

func loadAllClassified(dir string) ([]model.ClassifiedThread, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading classified dir: %w", err)
	}
	var all []model.ClassifiedThread
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var threads []model.ClassifiedThread
		if err := json.Unmarshal(data, &threads); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", e.Name(), err)
		}
		all = append(all, threads...)
	}
	return all, nil
}

func importThread(client *notionapi.Client, databaseID string, t model.ClassifiedThread) error {
	properties := threadProperties(t)
	blocks := threadBlocks(client, t)

	// Notion only accepts up to 100 children at page-creation time; the
	// rest must be appended afterwards.
	const maxCreateBlocks = 100
	first := blocks
	var rest []map[string]interface{}
	if len(blocks) > maxCreateBlocks {
		first = blocks[:maxCreateBlocks]
		rest = blocks[maxCreateBlocks:]
	}

	pageID, err := client.CreatePage(databaseID, properties, first)
	if err != nil {
		return err
	}

	for len(rest) > 0 {
		chunk := rest
		if len(chunk) > maxCreateBlocks {
			chunk = rest[:maxCreateBlocks]
		}
		if err := client.AppendBlocks(pageID, chunk); err != nil {
			return fmt.Errorf("appending remaining blocks: %w", err)
		}
		rest = rest[len(chunk):]
		time.Sleep(350 * time.Millisecond)
	}
	return nil
}

func threadProperties(t model.ClassifiedThread) map[string]interface{} {
	titleRuns := threadTitleRuns(t)
	author := ""
	if len(t.Messages) > 0 {
		author = t.Messages[0].Author
	}

	var participants []map[string]string
	for _, p := range t.Participants {
		if p == "" {
			continue
		}
		participants = append(participants, map[string]string{"name": truncate(p, 100)})
	}
	var tags []map[string]string
	for _, tag := range t.Tags {
		tags = append(tags, map[string]string{"name": tag})
	}

	props := map[string]interface{}{
		"Title": map[string]interface{}{
			"title": titleRuns,
		},
		"Channel": map[string]interface{}{
			"select": map[string]string{"name": truncate(t.Category, 100)},
		},
		"Source": map[string]interface{}{
			"select": map[string]string{"name": string(t.Source)},
		},
		"Tag": map[string]interface{}{
			"multi_select": tags,
		},
		"Author": map[string]interface{}{
			"rich_text": []notionapi.RichText{notionapi.Text(author)},
		},
		"Participants": map[string]interface{}{
			"multi_select": participants,
		},
		"Started At": map[string]interface{}{
			"date": map[string]string{"start": t.StartedAt.Format(time.RFC3339)},
		},
		"Last Updated": map[string]interface{}{
			"date": map[string]string{"start": t.LastUpdated.Format(time.RFC3339)},
		},
		"Message Count": map[string]interface{}{
			"number": len(t.Messages),
		},
		"Thread ID": map[string]interface{}{
			"rich_text": []notionapi.RichText{notionapi.Text(t.ChannelID + ":" + t.ThreadTs)},
		},
		"Migration ID": map[string]interface{}{
			"rich_text": []notionapi.RichText{notionapi.Text(t.MigrationID)},
		},
		"Has File": map[string]interface{}{
			"checkbox": t.HasFile,
		},
	}
	if t.Permalink != "" {
		props["Slack URL"] = map[string]interface{}{"url": t.Permalink}
	}
	return props
}

// threadTitleRuns builds the page title from the thread's first non-empty
// message text (the author is intentionally omitted since it already has
// its own "Author" property), preserving Slack text decorations.
func threadTitleRuns(t model.ClassifiedThread) []map[string]interface{} {
	return slackInlineRuns(threadTitlePlain(t))
}

// threadTitlePlain returns the same title text as threadTitleRuns but as a
// plain string (used for console/log output, e.g. the validate command).
func threadTitlePlain(t model.ClassifiedThread) string {
	for _, m := range t.Messages {
		if m.Text != "" {
			return truncate(m.Text, 200)
		}
	}
	return truncate(t.ChannelName+" ("+t.ThreadTs+")", 200)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// threadBlocks renders the full, unmodified conversation as Notion blocks:
// one paragraph per message (author, timestamp, text), followed by a file
// block (uploaded content) or a bookmark (Slack link fallback) for each
// attachment.
func threadBlocks(client *notionapi.Client, t model.ClassifiedThread) []map[string]interface{} {
	var blocks []map[string]interface{}
	for _, m := range t.Messages {
		label := m.Author
		if label == "" {
			label = "(unknown)"
		}
		header := fmt.Sprintf("%s — %s", label, m.Time.Format("2006-01-02 15:04"))
		blocks = append(blocks, paragraphBlock(header, true))
		if m.Text != "" {
			blocks = append(blocks, renderMessageBlocks(m.Text)...)
		}
		for _, f := range m.Files {
			blocks = append(blocks, fileBlock(client, f))
		}
	}
	return blocks
}

func paragraphBlock(text string, bold bool) map[string]interface{} {
	return map[string]interface{}{
		"object": "block",
		"type":   "paragraph",
		"paragraph": map[string]interface{}{
			"rich_text": []map[string]interface{}{
				{
					"type":        "text",
					"text":        map[string]string{"content": text},
					"annotations": map[string]bool{"bold": bold},
				},
			},
		},
	}
}

func fileBlock(client *notionapi.Client, f model.NormalizedFile) map[string]interface{} {
	if f.Downloaded && f.LocalPath != "" {
		uploadID, err := client.UploadFile(f.LocalPath, f.Name, f.Mimetype)
		if err == nil {
			return map[string]interface{}{
				"object": "block",
				"type":   "file",
				"file": map[string]interface{}{
					"type":        "file_upload",
					"file_upload": map[string]string{"id": uploadID},
				},
			}
		}
		fmt.Printf("    warning: failed to upload %s to Notion, falling back to link: %v\n", f.Name, err)
	}

	link := f.Permalink
	if link == "" {
		link = f.URLPrivateDownload
	}
	if link == "" {
		return paragraphBlock("[添付ファイル: "+f.Name+" (リンクなし)]", false)
	}
	return map[string]interface{}{
		"object": "block",
		"type":   "bookmark",
		"bookmark": map[string]interface{}{
			"url":     link,
			"caption": []notionapi.RichText{notionapi.Text(f.Name + "（Slackの元リンク。期限切れの可能性あり）")},
		},
	}
}

// chunkText splits long text into segments no longer than max runes, since
// Notion rich_text content has a 2000-character limit per segment.
func chunkText(s string, max int) []string {
	r := []rune(s)
	if len(r) <= max {
		return []string{s}
	}
	var chunks []string
	for len(r) > 0 {
		n := max
		if n > len(r) {
			n = len(r)
		}
		chunks = append(chunks, string(r[:n]))
		r = r[n:]
	}
	return chunks
}

// --- Slack mrkdwn -> Notion rich_text rendering ---

// codeFenceRe matches Slack ```code block``` fences, which are rendered as
// dedicated Notion "code" blocks rather than inline-formatted paragraphs.
var codeFenceRe = regexp.MustCompile("(?s)```(.*?)```")

// inlineTokenRe matches Slack's remaining inline mrkdwn decorations. Group
// layout (see indices used below): 1-2=link label/url (pre-converted by
// normalize.convertMrkdwn into "[label](url)"), 3=code span, 4=bold,
// 5=italic, 6=strikethrough.
var inlineTokenRe = regexp.MustCompile(
	`\[([^\]\n]*)\]\(([^)\n]*)\)` + `|` +
		"`([^`\n]+)`" + `|` +
		`\*([^*\n]+)\*` + `|` +
		`_([^_\n]+)_` + `|` +
		`~([^~\n]+)~`)

// renderMessageBlocks converts a (mrkdwn-resolved) message body into Notion
// blocks: ```code fences``` become "code" blocks, everything else becomes
// one or more paragraph blocks with Slack's bold/italic/strikethrough/code
// decorations and links reproduced as Notion rich_text annotations.
func renderMessageBlocks(text string) []map[string]interface{} {
	var blocks []map[string]interface{}
	last := 0
	for _, m := range codeFenceRe.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > last {
			blocks = append(blocks, renderPlainSegment(text[last:m[0]])...)
		}
		blocks = append(blocks, codeBlock(strings.TrimSpace(text[m[2]:m[3]])))
		last = m[1]
	}
	if last < len(text) {
		blocks = append(blocks, renderPlainSegment(text[last:])...)
	}
	return blocks
}

func renderPlainSegment(s string) []map[string]interface{} {
	s = strings.Trim(s, "\n")
	if s == "" {
		return nil
	}
	var blocks []map[string]interface{}
	for _, chunk := range chunkText(s, 1900) {
		blocks = append(blocks, richParagraphBlock(slackInlineRuns(chunk)))
	}
	return blocks
}

func richParagraphBlock(runs []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"object": "block",
		"type":   "paragraph",
		"paragraph": map[string]interface{}{
			"rich_text": runs,
		},
	}
}

func codeBlock(code string) map[string]interface{} {
	if code == "" {
		code = " "
	}
	return map[string]interface{}{
		"object": "block",
		"type":   "code",
		"code": map[string]interface{}{
			"rich_text": []map[string]interface{}{plainRun(code)},
			"language":  "plain text",
		},
	}
}

// slackInlineRuns parses Slack's inline mrkdwn (bold/italic/strikethrough/
// code/links) out of text and returns the equivalent Notion rich_text runs.
func slackInlineRuns(text string) []map[string]interface{} {
	matches := inlineTokenRe.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return []map[string]interface{}{plainRun(text)}
	}
	var runs []map[string]interface{}
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if start > last {
			if r := plainRun(text[last:start]); r != nil {
				runs = append(runs, r)
			}
		}
		switch {
		case m[2] != -1: // link: label=[2:3], url=[4:5]
			runs = append(runs, linkRun(text[m[2]:m[3]], text[m[4]:m[5]]))
		case m[6] != -1: // code span
			runs = append(runs, annotatedRun(text[m[6]:m[7]], map[string]bool{"code": true}))
		case m[8] != -1: // bold
			runs = append(runs, annotatedRun(text[m[8]:m[9]], map[string]bool{"bold": true}))
		case m[10] != -1: // italic
			runs = append(runs, annotatedRun(text[m[10]:m[11]], map[string]bool{"italic": true}))
		case m[12] != -1: // strikethrough
			runs = append(runs, annotatedRun(text[m[12]:m[13]], map[string]bool{"strikethrough": true}))
		}
		last = end
	}
	if last < len(text) {
		if r := plainRun(text[last:]); r != nil {
			runs = append(runs, r)
		}
	}
	if len(runs) == 0 {
		return []map[string]interface{}{plainRun(text)}
	}
	return runs
}

func plainRun(s string) map[string]interface{} {
	if s == "" {
		return nil
	}
	return map[string]interface{}{
		"type": "text",
		"text": map[string]interface{}{"content": s},
	}
}

func annotatedRun(s string, annotations map[string]bool) map[string]interface{} {
	return map[string]interface{}{
		"type":        "text",
		"text":        map[string]interface{}{"content": s},
		"annotations": annotations,
	}
}

func linkRun(label, url string) map[string]interface{} {
	if label == "" {
		label = url
	}
	return map[string]interface{}{
		"type": "text",
		"text": map[string]interface{}{
			"content": label,
			"link":    map[string]string{"url": url},
		},
	}
}
