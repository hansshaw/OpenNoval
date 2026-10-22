package planning

import (
	"strings"
	"testing"
)

func TestParseBookChapterTargetExplicitWholeBook(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "arabic common", text: "写一部东方玄幻，全书共80章。", want: 80},
		{name: "chinese common", text: "全书共八十章", want: 80},
		{name: "exact marker", text: "全书正好120章", want: 120},
		{name: "total marker", text: "全书总计一百二十章", want: 120},
		{name: "complete after count", text: "共80章完结", want: 80},
		{name: "book complete", text: "80章完本", want: 80},
		{name: "complete at chapter", text: "全书在第100章完结", want: 100},
		{name: "project form", text: "目标章节数量：必须为 80 章；所有卷、所有弧的真实 chapters 总数必须严格等于 80", want: 80},
		{name: "volumes plus total", text: "计划分三卷全书共一百四十章，第一卷必须有真实章节", want: 140},
		{name: "fullwidth digits", text: "全书共８０章", want: 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := ParseBookChapterTarget(tt.text)
			if err != nil {
				t.Fatalf("ParseBookChapterTarget: %v", err)
			}
			if !ok || got != tt.want {
				t.Fatalf("target = (%d, %v), want (%d, true)", got, ok, tt.want)
			}
		})
	}
}

func TestParseBookChapterTargetIgnoresRunLimitsAndScopedCounts(t *testing.T) {
	for _, text := range []string{
		"写到第80章",
		"先写到80章再停",
		"目前写到80章",
		"从第80章继续",
		"第一卷共20章",
		"每卷共20章",
		"本卷篇幅为20章",
		"全书约80章",
		"全书八十章左右",
		"全书共80-100章",
	} {
		t.Run(text, func(t *testing.T) {
			got, ok, err := ParseBookChapterTarget(text)
			if err != nil {
				t.Fatalf("ParseBookChapterTarget: %v", err)
			}
			if ok {
				t.Fatalf("unexpected target %d for %q", got, text)
			}
		})
	}
}

func TestParseBookChapterTargetHandlesNegationAndCorrections(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{text: "不是80章完结，而是100章完本", want: 100},
		{text: "全书共80章，后改为100章", want: 100},
		{text: "全书共80章，最终调整到一百二十章", want: 120},
		{text: "全书共80章，后来改为100章，最终改为120章", want: 120},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, ok, err := ParseBookChapterTarget(tt.text)
			if err != nil {
				t.Fatalf("ParseBookChapterTarget: %v", err)
			}
			if !ok || got != tt.want {
				t.Fatalf("target = (%d, %v), want (%d, true)", got, ok, tt.want)
			}
		})
	}
}

func TestParseBookChapterTargetRejectsConflictingTargets(t *testing.T) {
	_, ok, err := ParseBookChapterTarget("全书共80章，另一次方案为全书共100章")
	if ok || err == nil {
		t.Fatalf("conflict must fail explicitly, got ok=%v err=%v", ok, err)
	}
	if !strings.Contains(err.Error(), "80") || !strings.Contains(err.Error(), "100") {
		t.Fatalf("conflict error must include both targets: %v", err)
	}
}

func TestParseNumberChinese(t *testing.T) {
	tests := map[string]int{
		"十": 10, "十五": 15, "二十": 20, "八十": 80,
		"一百": 100, "一百零二": 102, "一百二十": 120,
		"两百": 200, "二千": 2000, "一万零二十": 10020,
		"二〇二四": 2024,
	}
	for raw, want := range tests {
		got, err := parseNumber(raw)
		if err != nil {
			t.Fatalf("parseNumber(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("parseNumber(%q) = %d, want %d", raw, got, want)
		}
	}
}
