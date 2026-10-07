// Package model defines the shared data structures that flow through the
// Slack -> Notion migration pipeline: raw Slack export data, normalized
// threads, and classified threads ready for Notion ingestion.
package model

import "time"

// SlackFile represents an attached file reference as found in Slack export
// JSON or the Slack Web API response. Only the fields the pipeline needs are
// kept; everything else from Slack's payload is intentionally dropped.
type SlackFile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Title              string `json:"title"`
	Mimetype           string `json:"mimetype"`
	Filetype           string `json:"filetype"`
	Size               int64  `json:"size"`
	URLPrivate         string `json:"url_private"`
	URLPrivateDownload string `json:"url_private_download"`
	Permalink          string `json:"permalink"`
}

// RawMessage is a loosely-typed representation of a single Slack message as
// stored in a per-day export JSON file (either the official export or the
// DM export produced by this tool).
type RawMessage struct {
	Type       string      `json:"type"`
	Subtype    string      `json:"subtype,omitempty"`
	User       string      `json:"user,omitempty"`
	Username   string      `json:"username,omitempty"`
	Text       string      `json:"text"`
	Ts         string      `json:"ts"`
	ThreadTs   string      `json:"thread_ts,omitempty"`
	ReplyCount int         `json:"reply_count,omitempty"`
	Files      []SlackFile `json:"files,omitempty"`
}

// SlackUser is the subset of users.json / users.info fields needed to
// resolve a user ID to a human-readable display name.
type SlackUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	Profile  struct {
		RealName    string `json:"real_name"`
		DisplayName string `json:"display_name"`
	} `json:"profile"`
	IsBot bool `json:"is_bot"`
}

// DisplayName returns the best available human-readable name for the user.
func (u SlackUser) DisplayName() string {
	switch {
	case u.Profile.DisplayName != "":
		return u.Profile.DisplayName
	case u.Profile.RealName != "":
		return u.Profile.RealName
	case u.RealName != "":
		return u.RealName
	case u.Name != "":
		return u.Name
	default:
		return u.ID
	}
}

// SlackChannel is the subset of channels.json fields needed to resolve a
// channel ID to a name.
type SlackChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Source identifies where a thread originated from.
type Source string

const (
	SourcePublicChannel Source = "public_channel"
	SourceDM            Source = "dm"
	SourceMPIM          Source = "mpim"
)

// NormalizedFile is a file attachment after normalization, with an optional
// local path if the file content was downloaded.
type NormalizedFile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Mimetype           string `json:"mimetype"`
	Filetype           string `json:"filetype"`
	Size               int64  `json:"size"`
	URLPrivateDownload string `json:"url_private_download,omitempty"`
	Permalink          string `json:"permalink,omitempty"`
	LocalPath          string `json:"local_path,omitempty"`
	Downloaded         bool   `json:"downloaded"`
}

// NormalizedMessage is a single Slack message after user-name resolution and
// mrkdwn-to-plain-text conversion.
type NormalizedMessage struct {
	AuthorID string           `json:"author_id"`
	Author   string           `json:"author"`
	Ts       string           `json:"ts"`
	Time     time.Time        `json:"time"`
	Text     string           `json:"text"`
	Files    []NormalizedFile `json:"files,omitempty"`
	IsSystem bool             `json:"is_system"`
}

// NormalizedThread groups one Slack thread (or a standalone message, treated
// as a single-message thread) into one unit, which becomes one Notion page.
type NormalizedThread struct {
	ChannelID    string              `json:"channel_id"`
	ChannelName  string              `json:"channel_name"`
	Source       Source              `json:"source"`
	ThreadTs     string              `json:"thread_ts"`
	Messages     []NormalizedMessage `json:"messages"`
	Participants []string            `json:"participants"`
	StartedAt    time.Time           `json:"started_at"`
	LastUpdated  time.Time           `json:"last_updated"`
	Permalink    string              `json:"permalink,omitempty"`
}

// ClassifiedThread is a NormalizedThread augmented with migration metadata
// and classification tags, ready to be imported into Notion.
type ClassifiedThread struct {
	NormalizedThread
	MigrationID string   `json:"migration_id"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	HasFile     bool     `json:"has_file"`
}

// MessageCount returns the number of messages in the thread.
func (t NormalizedThread) MessageCount() int {
	return len(t.Messages)
}
