package desktopui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// chapterWorkbenchItem joins the confirmed/planned directory with generated
// content. A planned chapter stays visible before the writer produces a file.
type chapterWorkbenchItem struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Words       int      `json:"words"`
	Source      string   `json:"source"`
	Status      string   `json:"status"`
	HasContent  bool     `json:"has_content"`
	HasDraft    bool     `json:"has_draft,omitempty"`
	CoreEvent   string   `json:"core_event,omitempty"`
	Hook        string   `json:"hook,omitempty"`
	Scenes      []string `json:"scenes,omitempty"`
	VolumeID    int      `json:"volume_id"`
	VolumeTitle string   `json:"volume_title"`
	ArcID       int      `json:"arc_id,omitempty"`
	ArcTitle    string   `json:"arc_title,omitempty"`
}

func chapterWorkbenchItems(st *store.Store, outline []domain.OutlineEntry, progress *domain.Progress) []chapterWorkbenchItem {
	items := make(map[int]chapterWorkbenchItem, len(outline))
	newItem := func(number int) chapterWorkbenchItem {
		return chapterWorkbenchItem{
			Number: number, Title: fmt.Sprintf("第 %d 章", number),
			Source: "planned", Status: "planned", VolumeID: 1, VolumeTitle: "正文",
		}
	}
	for _, entry := range outline {
		if entry.Chapter <= 0 {
			continue
		}
		item := newItem(entry.Chapter)
		if strings.TrimSpace(entry.Title) != "" {
			item.Title = entry.Title
		}
		item.CoreEvent, item.Hook, item.Scenes = entry.CoreEvent, entry.Hook, entry.Scenes
		if entry.VolumeID > 0 {
			item.VolumeID, item.VolumeTitle = entry.VolumeID, entry.VolumeTitle
		}
		item.ArcID, item.ArcTitle = entry.ArcID, entry.ArcTitle
		items[item.Number] = item
	}
	if progress != nil {
		for _, number := range progress.CompletedChapters {
			if number <= 0 {
				continue
			}
			item, ok := items[number]
			if !ok {
				item = newItem(number)
			}
			if title, err := st.Summaries.LoadSummaryTitle(number); err == nil && strings.TrimSpace(title) != "" {
				item.Title = title
			}
			item.Source, item.Status, item.HasContent = "final", "completed", true
			item.Words = progress.ChapterWordCounts[number]
			items[number] = item
		}
		if number := progress.InProgressChapter; number > 0 {
			if draft, err := st.Drafts.LoadDraft(number); err == nil && strings.TrimSpace(draft) != "" {
				item, ok := items[number]
				if !ok {
					item = newItem(number)
				}
				item.HasDraft = true
				// Rewriting a completed chapter must not replace its accepted
				// content or create a second, conflicting directory entry.
				if item.Status != "completed" {
					item.Source, item.Status, item.HasContent = "draft", "draft", true
					item.Words = domain.WordCount(draft)
				}
				items[number] = item
			}
		}
	}
	result := make([]chapterWorkbenchItem, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result
}
