// Package slackapi is a minimal Slack Web API client used to fetch direct
// message (DM) and group DM (MPIM) history using a user token. It only
// implements the handful of endpoints needed by this migration tool.
package slackapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const baseURL = "https://slack.com/api/"

// Client is a thin wrapper around Slack's Web API.
type Client struct {
	Token      string
	HTTPClient *http.Client
}

// New creates a Slack API client using the given user OAuth token
// (xoxp-...).
func New(token string) *Client {
	return &Client{
		Token:      token,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type apiError struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error"`
}

// call performs a GET request against the Slack Web API, retrying on rate
// limit (HTTP 429) responses by honoring the Retry-After header.
func (c *Client) call(method string, params url.Values, out interface{}) error {
	if params == nil {
		params = url.Values{}
	}
	for attempt := 0; attempt < 6; attempt++ {
		req, err := http.NewRequest(http.MethodGet, baseURL+method+"?"+params.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := 2
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if s, err := strconv.Atoi(ra); err == nil {
					wait = s
				}
			}
			resp.Body.Close()
			time.Sleep(time.Duration(wait) * time.Second)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}

		var ae apiError
		if err := json.Unmarshal(body, &ae); err == nil && !ae.Ok {
			return fmt.Errorf("slack api %s error: %s", method, ae.Error)
		}

		return json.Unmarshal(body, out)
	}
	return fmt.Errorf("slack api %s: exceeded retry attempts due to rate limiting", method)
}

// AuthTest calls auth.test and returns the workspace's Slack domain (e.g.
// "example.slack.com") and the authenticated user's ID.
func (c *Client) AuthTest() (domain string, userID string, err error) {
	var out struct {
		apiError
		URL    string `json:"url"`
		UserID string `json:"user_id"`
	}
	if err := c.call("auth.test", nil, &out); err != nil {
		return "", "", err
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		return "", out.UserID, nil
	}
	return u.Host, out.UserID, nil
}

// Conversation represents a DM or group-DM (MPIM) conversation.
type Conversation struct {
	ID     string `json:"id"`
	IsIM   bool   `json:"is_im"`
	IsMpim bool   `json:"is_mpim"`
	User   string `json:"user"` // for IM: the other participant's user ID
	Name   string `json:"name"` // for MPIM: an auto-generated name
}

// ListDMConversations returns all IM and MPIM conversations the
// authenticated user is a member of.
func (c *Client) ListDMConversations() ([]Conversation, error) {
	var all []Conversation
	cursor := ""
	for {
		params := url.Values{"types": {"im,mpim"}, "limit": {"200"}}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var out struct {
			apiError
			Channels         []Conversation `json:"channels"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := c.call("conversations.list", params, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Channels...)
		cursor = out.ResponseMetadata.NextCursor
		if cursor == "" {
			break
		}
		time.Sleep(300 * time.Millisecond) // stay well under rate limits
	}
	return all, nil
}

// RawHistoryMessage mirrors the fields of model.RawMessage as returned by
// Slack's conversations.history / conversations.replies endpoints.
type RawHistoryMessage struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype,omitempty"`
	User       string `json:"user,omitempty"`
	Text       string `json:"text"`
	Ts         string `json:"ts"`
	ThreadTs   string `json:"thread_ts,omitempty"`
	ReplyCount int    `json:"reply_count,omitempty"`
	Files      []struct {
		ID                 string `json:"id"`
		Name               string `json:"name"`
		Title              string `json:"title"`
		Mimetype           string `json:"mimetype"`
		Filetype           string `json:"filetype"`
		Size               int64  `json:"size"`
		URLPrivate         string `json:"url_private"`
		URLPrivateDownload string `json:"url_private_download"`
		Permalink          string `json:"permalink"`
	} `json:"files,omitempty"`
}

// History fetches all messages (across pagination) from a conversation
// within the optional [oldest, latest] unix-timestamp window. Pass "0" for
// no bound.
func (c *Client) History(channelID, oldest, latest string) ([]RawHistoryMessage, error) {
	var all []RawHistoryMessage
	cursor := ""
	for {
		params := url.Values{
			"channel": {channelID},
			"limit":   {"200"},
		}
		if oldest != "" && oldest != "0" {
			params.Set("oldest", oldest)
		}
		if latest != "" && latest != "0" {
			params.Set("latest", latest)
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var out struct {
			apiError
			Messages         []RawHistoryMessage `json:"messages"`
			HasMore          bool                `json:"has_more"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := c.call("conversations.history", params, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Messages...)
		if !out.HasMore || out.ResponseMetadata.NextCursor == "" {
			break
		}
		cursor = out.ResponseMetadata.NextCursor
		time.Sleep(300 * time.Millisecond)
	}
	return all, nil
}

// Replies fetches all messages in a thread (including the root message).
func (c *Client) Replies(channelID, threadTs string) ([]RawHistoryMessage, error) {
	var all []RawHistoryMessage
	cursor := ""
	for {
		params := url.Values{
			"channel": {channelID},
			"ts":      {threadTs},
			"limit":   {"200"},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		var out struct {
			apiError
			Messages         []RawHistoryMessage `json:"messages"`
			HasMore          bool                `json:"has_more"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := c.call("conversations.replies", params, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Messages...)
		if !out.HasMore || out.ResponseMetadata.NextCursor == "" {
			break
		}
		cursor = out.ResponseMetadata.NextCursor
		time.Sleep(300 * time.Millisecond)
	}
	return all, nil
}

// UsersInfo fetches a single user's profile.
func (c *Client) UsersInfo(userID string) (*UserInfo, error) {
	var out struct {
		apiError
		User UserInfo `json:"user"`
	}
	if err := c.call("users.info", url.Values{"user": {userID}}, &out); err != nil {
		return nil, err
	}
	return &out.User, nil
}

// UserInfo mirrors the fields of model.SlackUser.
type UserInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	Profile  struct {
		RealName    string `json:"real_name"`
		DisplayName string `json:"display_name"`
	} `json:"profile"`
	IsBot bool `json:"is_bot"`
}
