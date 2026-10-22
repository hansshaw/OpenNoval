package bootstrap

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestStyleReferenceCallbackRemainsRuntimeOnlyAcrossConfigCopies(t *testing.T) {
	selection := "initial"
	cfg := Config{StyleReferenceProfiles: func() ([]domain.NamedStyleReference, error) {
		return []domain.NamedStyleReference{{ID: selection, Name: "selected reference"}}, nil
	}}
	cloned := CloneConfig(cfg)
	selection = "updated"
	if cloned.StyleReferenceProfiles == nil {
		t.Fatal("config copy lost the live reference resolver")
	}
	refs, err := cloned.StyleReferenceProfiles()
	if err != nil || len(refs) != 1 || refs[0].ID != "updated" {
		t.Fatalf("config copy froze the old selection: %+v %v", refs, err)
	}
	raw, err := json.Marshal(cloned)
	if err != nil || strings.Contains(string(raw), "StyleReferenceProfiles") || strings.Contains(string(raw), "selected reference") {
		t.Fatalf("runtime style selection leaked into saved model config: %s %v", raw, err)
	}
}
