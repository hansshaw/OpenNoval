// Package planning owns deterministic planning contracts derived from user input.
package planning

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const numberExpr = `[0-9]+|[零〇一二两三四五六七八九十百千万]+`

var (
	targetLabelRE = regexp.MustCompile(
		`(?:目标章节(?:数量|总数)?|章节目标|总章节数|章节总数)\s*[：:]?\s*(?:必须|需要|要求)?\s*(?:为|是|定为|设为)?\s*(` + numberExpr + `)\s*(?:章|章节)`,
	)
	fullMarkerRE = regexp.MustCompile(
		`(?:全书|本书)\s*(?:(?:的)?(?:总章节数|章节总数|目标章节(?:数量|总数)?)\s*[：:]?\s*(?:必须|需要|要求)?\s*(?:为|是|定为|设为)?|(?:共|总共|共计|合计|总计|一共|正好|恰好|总篇幅(?:为|是)?|篇幅(?:为|是)?|目标(?:为|是)?|计划(?:为|是)?|定为|设为))\s*(?:有|写|写成|安排)?\s*(` + numberExpr + `)\s*(?:章|章节)`,
	)
	fullBareRE = regexp.MustCompile(
		`(?:全书|本书)\s*(` + numberExpr + `)\s*(?:章|章节)`,
	)
	totalNounRE = regexp.MustCompile(
		`(?:总章节数|章节总数|总篇幅|全书篇幅|篇幅)\s*[：:]?\s*(?:必须|需要|要求)?\s*(?:为|是|定为|设为)?\s*(` + numberExpr + `)\s*(?:章|章节)`,
	)
	totalMarkerRE = regexp.MustCompile(
		`(?:共|总共|共计|合计|总计|一共|正好|恰好)\s*(?:有|写|写成|安排)?\s*(` + numberExpr + `)\s*(?:章|章节)`,
	)
	completionRE = regexp.MustCompile(
		`(?:第\s*)?(` + numberExpr + `)\s*(?:章|章节)\s*(?:完结|完本|结束|收尾)`,
	)
	completeAtRE = regexp.MustCompile(
		`(?:完结|完本|结束)\s*(?:于|在)?\s*(?:第\s*)?(` + numberExpr + `)\s*(?:章|章节)`,
	)
	correctionRE = regexp.MustCompile(
		`(?:改为|改成|调整为|调整到|修订为|修正为|重定为|定为|设为|扩展到|扩成|增加到|增至|缩减到|缩成|减少到|压缩到|而是)\s*(?:全书|本书)?\s*(?:共|总共|共计|总计)?\s*(` + numberExpr + `)\s*(?:章|章节)`,
	)
	writeToRE = regexp.MustCompile(
		`(?:目前(?:已经)?|已经|已|先|只|暂时)?\s*(?:写到|写至|续写到|更新到|连载到)\s*(?:第\s*)?(` + numberExpr + `)\s*(?:章|章节)`,
	)
	continueFromRE = regexp.MustCompile(
		`从\s*(?:第\s*)?(` + numberExpr + `)\s*(?:章|章节)\s*(?:继续|接着写|开始写|恢复)`,
	)
	localScopeRE = regexp.MustCompile(
		`(?:每|本|该|第[\d零〇一二两三四五六七八九十百千万]+)(?:卷|分卷|弧|故事弧)`,
	)
	negationRE = regexp.MustCompile(
		`(?:不是|并非|而不是|不再|不按|并非为)[^，,。；;！？!?]{0,16}$`,
	)
)

type targetPattern struct {
	re         *regexp.Regexp
	fullBook   bool
	correction bool
}

var targetPatterns = []targetPattern{
	{re: targetLabelRE},
	{re: fullMarkerRE, fullBook: true},
	{re: fullBareRE, fullBook: true},
	{re: totalNounRE},
	{re: totalMarkerRE},
	{re: completionRE},
	{re: completeAtRE},
	{re: correctionRE, correction: true},
}

type targetCandidate struct {
	value      int
	start      int
	end        int
	correction bool
}

// ParseBookChapterTarget extracts an exact whole-book chapter target from a
// startup prompt. It intentionally ignores ranges, approximate lengths, and
// one-off run limits such as "写到第80章再停".
func ParseBookChapterTarget(text string) (int, bool, error) {
	text = normalizeDigits(text)
	var candidates []targetCandidate
	for _, clause := range splitClauses(text) {
		candidates = append(candidates, clauseCandidates(clause)...)
	}
	if len(candidates) == 0 {
		return 0, false, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].start != candidates[j].start {
			return candidates[i].start < candidates[j].start
		}
		return candidates[i].end < candidates[j].end
	})

	final := candidates[len(candidates)-1]
	if final.correction {
		return final.value, true, nil
	}
	for _, candidate := range candidates[:len(candidates)-1] {
		if candidate.value != final.value {
			return 0, false, fmt.Errorf(
				"全书目标章数存在冲突：检测到 %d 章和 %d 章，请明确最终目标",
				candidate.value, final.value,
			)
		}
	}
	return final.value, true, nil
}

func splitClauses(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '\n', '\r', '，', ',', '。', '；', ';', '！', '!', '？', '?':
			return true
		default:
			return false
		}
	})
}

func clauseCandidates(clause string) []targetCandidate {
	paused := pausedRanges(clause)
	bySpan := make(map[string]targetCandidate)
	for _, pattern := range targetPatterns {
		for _, match := range pattern.re.FindAllStringSubmatchIndex(clause, -1) {
			numberStart, numberEnd := match[2], match[3]
			if negated(clause, match[0]) ||
				hasApproximateSuffix(clause, match[1]) ||
				overlapsAny(numberStart, numberEnd, paused) ||
				(!pattern.fullBook && hasLocalScope(clause, match[0])) {
				continue
			}
			value, err := parseNumber(clause[numberStart:numberEnd])
			if err != nil || value <= 0 {
				continue
			}
			key := fmt.Sprintf("%d:%d", numberStart, numberEnd)
			existing, ok := bySpan[key]
			if !ok || (pattern.correction && !existing.correction) {
				bySpan[key] = targetCandidate{
					value:      value,
					start:      numberStart,
					end:        numberEnd,
					correction: pattern.correction,
				}
			}
		}
	}

	out := make([]targetCandidate, 0, len(bySpan))
	for _, candidate := range bySpan {
		out = append(out, candidate)
	}
	return out
}

func pausedRanges(clause string) [][2]int {
	var ranges [][2]int
	for _, re := range []*regexp.Regexp{writeToRE, continueFromRE} {
		for _, match := range re.FindAllStringSubmatchIndex(clause, -1) {
			ranges = append(ranges, [2]int{match[2], match[3]})
		}
	}
	return ranges
}

func overlapsAny(start, end int, ranges [][2]int) bool {
	for _, span := range ranges {
		if start < span[1] && end > span[0] {
			return true
		}
	}
	return false
}

func negated(clause string, matchStart int) bool {
	prefix := clause[:matchStart]
	reset := 0
	for _, marker := range []string{"而是", "改为", "改成", "调整为", "调整到", "修订为", "最终"} {
		if idx := strings.LastIndex(prefix, marker); idx >= 0 {
			if end := idx + len(marker); end > reset {
				reset = end
			}
		}
	}
	return negationRE.MatchString(prefix[reset:])
}

func hasApproximateSuffix(clause string, matchEnd int) bool {
	rest := strings.TrimSpace(clause[matchEnd:])
	for _, prefix := range []string{"左右", "上下", "以内", "以上", "以下", "开外"} {
		if strings.HasPrefix(rest, prefix) {
			return true
		}
	}
	return false
}

func hasLocalScope(clause string, matchStart int) bool {
	prefix := clause[:matchStart]
	fullBookAt := max(
		strings.LastIndex(prefix, "全书"),
		strings.LastIndex(prefix, "本书"),
	)
	matches := localScopeRE.FindAllStringIndex(prefix, -1)
	if len(matches) == 0 {
		return false
	}
	return matches[len(matches)-1][1] > fullBookAt
}

func normalizeDigits(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '０' && r <= '９':
			return '0' + (r - '０')
		default:
			return r
		}
	}, text)
}

func parseNumber(raw string) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty number")
	}
	if raw[0] >= '0' && raw[0] <= '9' {
		return strconv.Atoi(raw)
	}
	digits := map[rune]int{
		'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
	}
	units := map[rune]int{
		'十': 10, '百': 100, '千': 1000, '万': 10000,
	}
	hasUnit := strings.ContainsAny(raw, "十百千万")
	if !hasUnit {
		value := 0
		for _, r := range raw {
			digit, ok := digits[r]
			if !ok {
				return 0, fmt.Errorf("invalid Chinese numeral %q", raw)
			}
			value = value*10 + digit
		}
		return value, nil
	}

	total, section, number := 0, 0, 0
	for _, r := range raw {
		if digit, ok := digits[r]; ok {
			number = digit
			continue
		}
		unit, ok := units[r]
		if !ok {
			return 0, fmt.Errorf("invalid Chinese numeral %q", raw)
		}
		if unit < 10000 {
			if number == 0 {
				number = 1
			}
			section += number * unit
			number = 0
			continue
		}
		section += number
		if section == 0 {
			section = 1
		}
		total += section * unit
		section, number = 0, 0
	}
	return total + section + number, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
