package pipeline

import (
	"fmt"

	"slack2notion/internal/notionapi"
)

// NotionDatabaseTitle is the name of the Notion database created under the
// "From Slack" parent page.
const NotionDatabaseTitle = "Slack Archive"

// RunCreateSchema creates the "Slack Archive" database under the given
// parent page ID with the property schema described in the migration plan,
// and returns its database ID for use in subsequent import-notion runs.
func RunCreateSchema(client *notionapi.Client, parentPageID string) (string, error) {
	properties := map[string]interface{}{
		"Title": map[string]interface{}{
			"title": map[string]interface{}{},
		},
		"Channel": map[string]interface{}{
			"select": map[string]interface{}{},
		},
		"Source": map[string]interface{}{
			"select": map[string]interface{}{
				"options": []map[string]string{
					{"name": "public_channel"},
					{"name": "dm"},
					{"name": "mpim"},
				},
			},
		},
		"Tag": map[string]interface{}{
			"multi_select": map[string]interface{}{
				"options": []map[string]string{
					{"name": TagText},
					{"name": TagImage},
					{"name": TagVideo},
					{"name": TagFile},
					{"name": TagLink},
					{"name": TagReminder},
					{"name": TagSystem},
					{"name": TagOther},
				},
			},
		},
		"Author": map[string]interface{}{
			"rich_text": map[string]interface{}{},
		},
		"Participants": map[string]interface{}{
			"multi_select": map[string]interface{}{},
		},
		"Started At": map[string]interface{}{
			"date": map[string]interface{}{},
		},
		"Last Updated": map[string]interface{}{
			"date": map[string]interface{}{},
		},
		"Message Count": map[string]interface{}{
			"number": map[string]interface{}{},
		},
		"Slack URL": map[string]interface{}{
			"url": map[string]interface{}{},
		},
		"Thread ID": map[string]interface{}{
			"rich_text": map[string]interface{}{},
		},
		"Migration ID": map[string]interface{}{
			"rich_text": map[string]interface{}{},
		},
		"Has File": map[string]interface{}{
			"checkbox": map[string]interface{}{},
		},
	}

	dbID, err := client.CreateDatabase(parentPageID, NotionDatabaseTitle, properties)
	if err != nil {
		return "", fmt.Errorf("creating Notion database: %w", err)
	}
	fmt.Printf("created Notion database %q with ID: %s\n", NotionDatabaseTitle, dbID)
	fmt.Println("set this as NOTION_DATABASE_ID for subsequent commands")
	return dbID, nil
}
