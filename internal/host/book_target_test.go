package host

import (
	"strings"
	"testing"

	storepkg "github.com/hansshaw/OpenNovel/internal/store"
)

func TestStartPreparedRejectsConflictingBookTargetsBeforeReset(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	h := &Host{store: st}

	err := h.StartPrepared("全书共80章，后来说不是80章，全书共100章")
	if err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("StartPrepared must reject conflicting targets, got %v", err)
	}
}

func TestRecordExplicitBookTargetPersistsPromptContract(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	h := &Host{store: st}

	if err := h.recordExplicitBookTarget("写一部悬疑小说，全书共一百二十章"); err != nil {
		t.Fatalf("recordExplicitBookTarget: %v", err)
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		t.Fatalf("Load run meta: %v", err)
	}
	if meta == nil || meta.TargetChapters != 120 {
		t.Fatalf("target contract = %+v, want 120", meta)
	}
}

func TestRecordExplicitBookTargetPreservesFormTargetWithoutPromptTarget(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	if err := st.RunMeta.SetTargetChapters(90); err != nil {
		t.Fatalf("SetTargetChapters: %v", err)
	}
	h := &Host{store: st}

	if err := h.recordExplicitBookTarget("先写到第20章再停"); err != nil {
		t.Fatalf("recordExplicitBookTarget: %v", err)
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		t.Fatalf("Load run meta: %v", err)
	}
	if meta == nil || meta.TargetChapters != 90 {
		t.Fatalf("form target must be preserved, got %+v", meta)
	}
}
