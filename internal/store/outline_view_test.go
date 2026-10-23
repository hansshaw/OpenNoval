package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
)

func TestOutlineViewRecoversLegacyLayeredMembershipWithoutWriting(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	// Old flat projections did not carry volume membership; source indexes
	// could also be absent. Reading must neither migrate files nor approvals.
	legacy := `[{"title":"启程","arcs":[{"title":"出发","chapters":[{"title":"灯火","volume_id":99,"arc_id":99}]},{"title":"远方","estimated_chapters":12}]},{"title":"归来","arcs":[{"title":"重逢","chapters":[{"title":"归人"}]}]}]`
	if err := s.Outline.io.WriteFileUnlocked("layered_outline.json", []byte(legacy)); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SaveOutline([]domain.OutlineEntry{{Chapter: 1, Title: "旧派生标题"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Outline.SavePremise("旧项目故事前提"); err != nil {
		t.Fatal(err)
	}
	before, err := s.PlanningFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Outline.LoadOutlineView()
	if err != nil {
		t.Fatal(err)
	}
	if len(view) != 2 || view[0].Chapter != 1 || view[1].Chapter != 2 {
		t.Fatalf("only real planned chapters should be projected: %+v", view)
	}
	if view[0].Title != "灯火" || view[0].VolumeID != 1 || view[0].VolumeTitle != "启程" || view[0].ArcID != 1 || view[0].ArcTitle != "出发" {
		t.Fatalf("first chapter membership = %+v", view[0])
	}
	if view[1].VolumeID != 2 || view[1].VolumeTitle != "归来" || view[1].ArcID != 1 || view[1].ArcTitle != "重逢" {
		t.Fatalf("second volume membership = %+v", view[1])
	}
	after, err := s.PlanningFingerprint()
	if err != nil || before != after {
		t.Fatalf("read changed approval fingerprint: %q -> %q, %v", before, after, err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(), "layered_outline.json"))
	if err != nil || string(data) != legacy {
		t.Fatalf("read changed legacy file: %s, %v", data, err)
	}
}

func TestOutlineViewPreservesExplicitFlatVolumesAndDefaultsLegacy(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	entries := []domain.OutlineEntry{
		{Chapter: 1, Title: "旧章"},
		{Chapter: 2, Title: "出发", VolumeID: 2, VolumeTitle: "远行"},
		{Chapter: 3, Title: "到达", VolumeID: 2},
	}
	if err := s.Outline.SaveOutline(entries); err != nil {
		t.Fatal(err)
	}
	view, err := s.Outline.LoadOutlineView()
	if err != nil {
		t.Fatal(err)
	}
	if len(view) != 3 || view[0].VolumeID != 1 || view[0].VolumeTitle != "正文" || view[1].VolumeID != 2 || view[2].VolumeTitle != "远行" {
		t.Fatalf("flat membership = %+v", view)
	}
	persisted, err := s.Outline.LoadOutline()
	if err != nil || !reflect.DeepEqual(persisted, entries) {
		t.Fatalf("display defaults must not modify persisted plan: %+v, %v", persisted, err)
	}
	markdown, err := os.ReadFile(filepath.Join(s.Dir(), "outline.md"))
	if err != nil || !strings.Contains(string(markdown), "## 第 2 卷：远行") || strings.Count(string(markdown), "第 2 卷") != 1 {
		t.Fatalf("flat volume marker missing: %s, %v", markdown, err)
	}
}

func TestSaveLayeredOutlineIncludesMembershipInFlatProjection(t *testing.T) {
	s := setupLayered(t, []domain.VolumeOutline{{Title: "初见", Arcs: []domain.ArcOutline{{Title: "破局", Chapters: []domain.OutlineEntry{{Title: "新章"}}}}}})
	flat, err := s.Outline.LoadOutline()
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) != 1 || flat[0].VolumeID != 1 || flat[0].VolumeTitle != "初见" || flat[0].ArcID != 1 || flat[0].ArcTitle != "破局" {
		t.Fatalf("persisted flat membership = %+v", flat)
	}
}
