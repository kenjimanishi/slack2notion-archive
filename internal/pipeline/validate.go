package pipeline

import (
	"fmt"
	"math/rand"
	"sort"

	"slack2notion/internal/model"
)

// ValidateConfig configures the validate stage.
type ValidateConfig struct {
	ClassifiedDir string
	SampleSize    int // number of sample threads to print per tag/source
}

// RunValidate reports aggregate counts (threads, messages, files, per-tag
// and per-source breakdowns) from the classified data, and prints a random
// sample of threads per tag so a human can spot-check classification
// quality before or after importing into Notion.
func RunValidate(cfg ValidateConfig) error {
	threads, err := loadAllClassified(cfg.ClassifiedDir)
	if err != nil {
		return err
	}

	totalMessages := 0
	totalFiles := 0
	bySource := map[model.Source]int{}
	byTag := map[string][]model.ClassifiedThread{}

	for _, t := range threads {
		totalMessages += len(t.Messages)
		bySource[t.Source]++
		for _, m := range t.Messages {
			totalFiles += len(m.Files)
		}
		for _, tag := range t.Tags {
			byTag[tag] = append(byTag[tag], t)
		}
	}

	fmt.Println("=== Validation Report ===")
	fmt.Printf("Total threads:  %d\n", len(threads))
	fmt.Printf("Total messages: %d\n", totalMessages)
	fmt.Printf("Total files:    %d\n", totalFiles)
	fmt.Println()
	fmt.Println("By source:")
	for src, count := range bySource {
		fmt.Printf("  %-15s %d\n", src, count)
	}
	fmt.Println()
	fmt.Println("By tag:")
	var tagNames []string
	for tag := range byTag {
		tagNames = append(tagNames, tag)
	}
	sort.Strings(tagNames)
	for _, tag := range tagNames {
		fmt.Printf("  %-12s %d\n", tag, len(byTag[tag]))
	}

	sampleSize := cfg.SampleSize
	if sampleSize <= 0 {
		sampleSize = 2
	}
	fmt.Println()
	fmt.Println("=== Sample threads per tag (for manual spot-check) ===")
	for _, tag := range tagNames {
		items := byTag[tag]
		fmt.Printf("\n[%s] (%d threads)\n", tag, len(items))
		idxs := rand.Perm(len(items))
		for i := 0; i < sampleSize && i < len(items); i++ {
			t := items[idxs[i]]
			fmt.Printf("  - %s | %s | %s\n", t.ChannelName, t.StartedAt.Format("2006-01-02"), threadTitlePlain(t))
		}
	}

	return nil
}
