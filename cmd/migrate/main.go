// Command migrate is the unified CLI entrypoint for the Slack -> Notion
// migration pipeline. Each stage is a subcommand and can be re-run
// independently; intermediate data is kept on disk so later stages never
// need to re-fetch from Slack unless explicitly asked to.
package main

import (
	"flag"
	"fmt"
	"os"

	"slack2notion/internal/fileassets"
	"slack2notion/internal/notionapi"
	"slack2notion/internal/pipeline"
)

const usage = `migrate - Slack to Notion archive migration tool

Usage:
  migrate export-dm   [--since YYYY-MM-DD] [--until YYYY-MM-DD]
                      (default: fetch entire DM history; flags narrow the range)
  migrate normalize
  migrate classify
  migrate fetch-files
  migrate schema
  migrate import-notion [--pilot N]
  migrate validate

Environment variables (can also be set via a .env file in the working
directory; real environment variables always take precedence):
  SLACK_USER_TOKEN       required for export-dm (xoxp-... user token);
                         also used by fetch-files to authenticate downloads
                         of DM-sourced attachments
  SLACK_WORKSPACE_DOMAIN optional override for permalink generation, e.g. example.slack.com
  SLACK_EXPORT_DIR       path to the official export dir (default: ./slack_export)
  NOTION_TOKEN           required for schema / import-notion (Notion connection's API token, ntn_...)
  NOTION_PARENT_PAGE_ID  required for schema (the "From Slack" page ID, shared with the connection)
  NOTION_DATABASE_ID     required for import-notion (printed by the schema command)
  NOTION_VERSION         optional Notion-Version header override (default 2022-06-28)
`

func main() {
	loadDotEnv(".env")

	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "export-dm":
		err = runExportDM(args)
	case "normalize":
		err = runNormalize(args)
	case "classify":
		err = runClassify(args)
	case "fetch-files":
		err = runFetchFiles(args)
	case "schema":
		err = runSchema(args)
	case "import-notion":
		err = runImportNotion(args)
	case "validate":
		err = runValidate(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		fmt.Print(usage)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func exportDir() string {
	if d := os.Getenv("SLACK_EXPORT_DIR"); d != "" {
		return d
	}
	return "./slack_export"
}

func runExportDM(args []string) error {
	fs := flag.NewFlagSet("export-dm", flag.ExitOnError)
	since := fs.String("since", "", "oldest date to fetch (YYYY-MM-DD); empty = no lower bound")
	until := fs.String("until", "", "newest date to fetch (YYYY-MM-DD); empty = up to now")
	fs.Parse(args)

	return pipeline.RunExportDM(pipeline.ExportDMConfig{
		Token:     os.Getenv("SLACK_USER_TOKEN"),
		OutDir:    "data/raw/dm",
		SinceDate: *since,
		UntilDate: *until,
	})
}

func runNormalize(args []string) error {
	return pipeline.RunNormalize(pipeline.NormalizeConfig{
		ExportDir: exportDir(),
		DMRawDir:  "data/raw/dm",
		OutDir:    "data/normalized",
	})
}

func runClassify(args []string) error {
	return pipeline.RunClassify(pipeline.ClassifyConfig{
		NormalizedDir: "data/normalized",
		OutDir:        "data/classified",
	})
}

func runFetchFiles(args []string) error {
	return pipeline.RunFetchFiles(pipeline.FetchFilesConfig{
		ClassifiedDir: "data/classified",
		FilesDir:      "data/files",
		Budget:        fileassets.DefaultBudget,
		SlackToken:    os.Getenv("SLACK_USER_TOKEN"),
	})
}

func runSchema(args []string) error {
	token := os.Getenv("NOTION_TOKEN")
	parentPageID := os.Getenv("NOTION_PARENT_PAGE_ID")
	if token == "" || parentPageID == "" {
		return fmt.Errorf("NOTION_TOKEN and NOTION_PARENT_PAGE_ID must be set")
	}
	client := notionapi.New(token)
	_, err := pipeline.RunCreateSchema(client, parentPageID)
	return err
}

func runImportNotion(args []string) error {
	fs := flag.NewFlagSet("import-notion", flag.ExitOnError)
	pilot := fs.Int("pilot", 0, "only import the first N threads (chronologically), for a trial run")
	fs.Parse(args)

	token := os.Getenv("NOTION_TOKEN")
	databaseID := os.Getenv("NOTION_DATABASE_ID")
	if token == "" || databaseID == "" {
		return fmt.Errorf("NOTION_TOKEN and NOTION_DATABASE_ID must be set")
	}
	client := notionapi.New(token)

	return pipeline.RunImportNotion(pipeline.ImportNotionConfig{
		ClassifiedDir: "data/classified",
		DatabaseID:    databaseID,
		Client:        client,
		Pilot:         *pilot,
	})
}

func runValidate(args []string) error {
	return pipeline.RunValidate(pipeline.ValidateConfig{
		ClassifiedDir: "data/classified",
		SampleSize:    2,
	})
}
