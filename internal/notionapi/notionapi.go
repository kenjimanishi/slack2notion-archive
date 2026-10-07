// Package notionapi is a minimal Notion API client implementing only the
// endpoints needed by this migration tool: creating a database, querying by
// a property value (for idempotent re-runs), creating pages with property
// values and body blocks, and appending extra blocks/uploading files.
package notionapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const baseURL = "https://api.notion.com/v1/"

// defaultAPIVersion is used unless overridden via Client.APIVersion. File
// uploads require a Notion-Version recent enough to support the File Upload
// API (introduced 2025); override with the NOTION_VERSION env var if the
// database/page endpoints need a different version than file uploads.
const defaultAPIVersion = "2022-06-28"

// Client is a thin wrapper around the Notion REST API.
type Client struct {
	Token      string
	APIVersion string
	HTTPClient *http.Client
}

// New creates a Notion API client using the given connection's API token.
func New(token string) *Client {
	version := os.Getenv("NOTION_VERSION")
	if version == "" {
		version = defaultAPIVersion
	}
	return &Client{
		Token:      token,
		APIVersion: version,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) request(method, path string, body interface{}, out interface{}) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	for attempt := 0; attempt < 6; attempt++ {
		req, err := http.NewRequest(method, baseURL+path, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Notion-Version", c.APIVersion)
		req.Header.Set("Content-Type", "application/json")

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
			if reader != nil {
				data, _ := json.Marshal(body)
				reader = bytes.NewReader(data)
			}
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}

		if resp.StatusCode >= 300 {
			return fmt.Errorf("notion api %s %s failed: %s: %s", method, path, resp.Status, string(respBody))
		}

		if out == nil {
			return nil
		}
		return json.Unmarshal(respBody, out)
	}
	return fmt.Errorf("notion api %s %s: exceeded retry attempts due to rate limiting", method, path)
}

// RichText is a minimal Notion rich_text object.
type RichText struct {
	Type string `json:"type"`
	Text struct {
		Content string `json:"content"`
		Link    *struct {
			URL string `json:"url"`
		} `json:"link,omitempty"`
	} `json:"text"`
}

// Text builds a plain rich_text segment, splitting content into
// Notion's 2000-character-per-segment limit if necessary is the caller's
// responsibility (see notionutil.Chunk for long bodies).
func Text(s string) RichText {
	rt := RichText{Type: "text"}
	rt.Text.Content = s
	return rt
}

// CreateDatabase creates a new database as a child of parentPageID with the
// given title and property schema, returning the new database's ID.
func (c *Client) CreateDatabase(parentPageID, title string, properties map[string]interface{}) (string, error) {
	body := map[string]interface{}{
		"parent":     map[string]string{"type": "page_id", "page_id": parentPageID},
		"title":      []RichText{Text(title)},
		"properties": properties,
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.request(http.MethodPost, "databases", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// QueryDatabaseByRichText searches a database for a page whose property
// (must be a rich_text or title property) equals the given value. Returns
// the page ID if found, or "" if not found.
func (c *Client) QueryDatabaseByRichText(databaseID, property, value string) (string, error) {
	body := map[string]interface{}{
		"filter": map[string]interface{}{
			"property": property,
			"rich_text": map[string]string{
				"equals": value,
			},
		},
		"page_size": 1,
	}
	var out struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := c.request(http.MethodPost, "databases/"+databaseID+"/query", body, &out); err != nil {
		return "", err
	}
	if len(out.Results) == 0 {
		return "", nil
	}
	return out.Results[0].ID, nil
}

// CreatePage creates a new page in the given database with the given
// property values and initial children blocks (max 100 blocks; use
// AppendBlocks for more).
func (c *Client) CreatePage(databaseID string, properties map[string]interface{}, children []map[string]interface{}) (string, error) {
	body := map[string]interface{}{
		"parent":     map[string]string{"type": "database_id", "database_id": databaseID},
		"properties": properties,
	}
	if len(children) > 0 {
		body["children"] = children
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.request(http.MethodPost, "pages", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// AppendBlocks appends additional child blocks to an existing page/block
// (used when a thread has more than 100 messages, beyond Notion's
// create-time children limit).
func (c *Client) AppendBlocks(blockID string, children []map[string]interface{}) error {
	body := map[string]interface{}{"children": children}
	return c.request(http.MethodPatch, "blocks/"+blockID+"/children", body, nil)
}

// UpdatePageProperties updates only the properties of an existing page
// (used to refresh classification tags on re-runs without recreating the
// page or its body).
func (c *Client) UpdatePageProperties(pageID string, properties map[string]interface{}) error {
	body := map[string]interface{}{"properties": properties}
	return c.request(http.MethodPatch, "pages/"+pageID, body, nil)
}

// CreateFileUpload starts a Notion Direct Upload (single-part, <=20MiB) and
// returns its upload ID and upload URL.
func (c *Client) CreateFileUpload(filename, contentType string) (id string, uploadURL string, err error) {
	body := map[string]interface{}{
		"filename":     filename,
		"content_type": contentType,
	}
	var out struct {
		ID        string `json:"id"`
		UploadURL string `json:"upload_url"`
	}
	if err := c.request(http.MethodPost, "file_uploads", body, &out); err != nil {
		return "", "", err
	}
	return out.ID, out.UploadURL, nil
}

// SendFileUpload streams the local file's contents to Notion as the
// multipart/form-data payload the Direct Upload "send" endpoint expects.
func (c *Client) SendFileUpload(uploadID, localPath, contentType string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filepath.Base(localPath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"file_uploads/"+uploadID+"/send", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Notion-Version", c.APIVersion)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notion file upload send failed: %s: %s", resp.Status, string(respBody))
	}
	return nil
}

// UploadFile is a convenience wrapper that creates a file upload object,
// sends its content, and returns the file_upload ID ready to be referenced
// in a block or property. Files over Notion's 20MiB single-request limit
// are rejected by the API; callers should pre-filter by size.
func (c *Client) UploadFile(localPath, filename, contentType string) (string, error) {
	id, uploadURL, err := c.CreateFileUpload(filename, contentType)
	if err != nil {
		return "", fmt.Errorf("creating file upload: %w", err)
	}
	_ = uploadURL // the SDK always sends to the canonical /send endpoint
	if err := c.SendFileUpload(id, localPath, contentType); err != nil {
		return "", fmt.Errorf("sending file upload content: %w", err)
	}
	return id, nil
}
