package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/voocel/ainovel-cli/internal/host"
)

// outlineGridThreshold 扁平大纲切换多列的章节阈值。
// short tier 上限 25 章，20 以下单列一屏装得下、且能保留"进行中"徽标。
// 分层大纲始终按卷弧分组，不进入扁平网格。
const outlineGridThreshold = 20

// renderOutlineSection 分层大纲按卷弧结构展示，扁平大纲按章节数选单列或多列。
func renderOutlineSection(snap host.UISnapshot, contentW int) string {
	if snap.Layered {
		return renderLayeredOutlineList(snap, contentW)
	}
	if len(snap.Outline) < outlineGridThreshold {
		return renderOutlineList(snap, contentW)
	}
	return renderOutlineGrid(snap, contentW)
}

// renderLayeredOutlineList 显示完整规划的每个卷、弧与章节。
// 章节仍来自同一份扁平投影，但按连续归属分组，避免把多卷目录误读为单卷流水账。
func renderLayeredOutlineList(snap host.UISnapshot, contentW int) string {
	if len(snap.Outline) == 0 {
		return ""
	}

	var b strings.Builder
	currentVolume, currentArc := -1, -1
	volumeStyle := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	arcStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dividerStyle := lipgloss.NewStyle().Foreground(colorDim)

	for i, e := range snap.Outline {
		volumeID := max(1, e.VolumeID)
		arcID := max(1, e.ArcID)
		if volumeID != currentVolume {
			if i > 0 {
				b.WriteString("\n")
			}
			currentVolume = volumeID
			currentArc = -1
			title := strings.TrimSpace(e.VolumeTitle)
			if title == "" {
				title = "未命名卷"
			}
			heading := fmt.Sprintf("第%d卷 · %s", volumeID, title)
			b.WriteString(volumeStyle.Render(truncate(heading, contentW)))
			b.WriteString("\n")
			b.WriteString(dividerStyle.Render(strings.Repeat("─", max(8, min(contentW, lipgloss.Width(heading))))))
			b.WriteString("\n")
		}
		if volumeID != currentVolume || arcID != currentArc {
			currentArc = arcID
			title := strings.TrimSpace(e.ArcTitle)
			if title == "" {
				title = "未命名弧"
			}
			count := outlineArcChapterCount(snap.Outline, i)
			heading := fmt.Sprintf("  第%d弧 · %s（%d章）", arcID, title, count)
			b.WriteString(arcStyle.Render(truncate(heading, contentW)))
			b.WriteString("\n")
		}
		b.WriteString(renderLayeredOutlineChapter(e, snap, contentW))
		b.WriteString("\n")
	}
	return b.String()
}

func outlineArcChapterCount(entries []host.OutlineSnapshot, start int) int {
	if start < 0 || start >= len(entries) {
		return 0
	}
	first := entries[start]
	count := 0
	for i := start; i < len(entries); i++ {
		if max(1, entries[i].VolumeID) != max(1, first.VolumeID) ||
			max(1, entries[i].ArcID) != max(1, first.ArcID) {
			break
		}
		count++
	}
	return count
}

func renderLayeredOutlineChapter(e host.OutlineSnapshot, snap host.UISnapshot, contentW int) string {
	ch := fmt.Sprintf("%2d", e.Chapter)
	var marker, chStyle string
	titleStyle := lipgloss.NewStyle().Foreground(colorMuted)
	switch {
	case snap.CompletedCount >= e.Chapter:
		marker = lipgloss.NewStyle().Foreground(colorSuccess).Render("●")
		chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
		titleStyle = cardContentStyle
	case snap.InProgressChapter == e.Chapter:
		marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸")
		chStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(ch)
		titleStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	default:
		marker = lipgloss.NewStyle().Foreground(colorDim).Render("○")
		chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
	}
	titleW := max(1, contentW-7)
	return "    " + marker + " " + chStyle + " " + titleStyle.Render(truncateWidth(e.Title, titleW))
}

// renderOutlineList 单列章节列表（短篇用）。每行尾部带"进行中"徽标，垂直阅读节奏更接近目录。
func renderOutlineList(snap host.UISnapshot, contentW int) string {
	var b strings.Builder
	for _, e := range snap.Outline {
		ch := fmt.Sprintf("%2d", e.Chapter)
		var marker, chStyle string
		titleStyle := cardContentStyle
		switch {
		case snap.CompletedCount >= e.Chapter:
			marker = lipgloss.NewStyle().Foreground(colorSuccess).Render("●")
			chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
		case snap.InProgressChapter == e.Chapter:
			marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸")
			chStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(ch)
			titleStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
		default:
			marker = lipgloss.NewStyle().Foreground(colorDim).Render("○")
			chStyle = lipgloss.NewStyle().Foreground(colorDim).Render(ch)
			titleStyle = lipgloss.NewStyle().Foreground(colorMuted)
		}
		title := truncate(e.Title, contentW-6)
		line := marker + chStyle + " " + titleStyle.Render(title)
		if snap.InProgressChapter == e.Chapter {
			line += lipgloss.NewStyle().Foreground(colorAccent).Italic(true).Render(" 进行中")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// renderOutlineGrid 把大纲章节按"列优先"填充为多列网格，避免宽屏单列大量留白。
// 列数按 contentW 自适应（1-4），列内章节连续递增（"读完一列再读下一列"）。
// 与单列布局的取舍：放弃尾部" 进行中"徽标——多列下徽标会破坏列对齐，
// 且 ▸ 标记 + 金色 + 左侧概览栏的"写作中 第 N 章"已经把进行中信息说清楚。
func renderOutlineGrid(snap host.UISnapshot, contentW int) string {
	n := len(snap.Outline)
	if n == 0 {
		return ""
	}
	chNumW := 2
	titleW := 0
	for _, e := range snap.Outline {
		if w := len(strconv.Itoa(e.Chapter)); w > chNumW {
			chNumW = w
		}
		if w := lipgloss.Width(e.Title); w > titleW {
			titleW = w
		}
	}
	// 标题宽度上限 14（约 7 个汉字）；偶尔出现的长标题截断，避免一两个长标题撑大全体 cell
	if titleW > 14 {
		titleW = 14
	} else if titleW < 4 {
		titleW = 4
	}
	cellW := 3 + chNumW + titleW // marker(1) + 空格(1) + 章号 + 空格(1) + 标题
	gutter := 4
	cols := (contentW + gutter) / (cellW + gutter)
	if cols < 1 {
		cols = 1
	} else if cols > 4 {
		cols = 4
	}
	rows := (n + cols - 1) / cols

	var b strings.Builder
	cellStyle := lipgloss.NewStyle().Width(cellW)
	gutterStr := strings.Repeat(" ", gutter)
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			idx := c*rows + r
			if idx >= n {
				break
			}
			cell := renderOutlineCell(snap.Outline[idx], snap, chNumW, titleW)
			// 后续列还有 cell 时按 cellW 补齐 + gutter；否则当前 cell 是行尾不补
			if c < cols-1 && (c+1)*rows+r < n {
				b.WriteString(cellStyle.Render(cell))
				b.WriteString(gutterStr)
			} else {
				b.WriteString(cell)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderOutlineCell 渲染单个章节 cell：完成（绿●）/ 进行中（金▸）/ 未开始（暗○）。
func renderOutlineCell(e host.OutlineSnapshot, snap host.UISnapshot, chNumW, titleW int) string {
	chStr := fmt.Sprintf("%*d", chNumW, e.Chapter)
	title := truncateWidth(e.Title, titleW)
	var marker, chRendered, titleRendered string
	switch {
	case snap.CompletedCount >= e.Chapter:
		marker = lipgloss.NewStyle().Foreground(colorSuccess).Render("●")
		chRendered = lipgloss.NewStyle().Foreground(colorDim).Render(chStr)
		titleRendered = cardContentStyle.Render(title)
	case snap.InProgressChapter == e.Chapter:
		marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("▸")
		chRendered = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(chStr)
		titleRendered = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(title)
	default:
		marker = lipgloss.NewStyle().Foreground(colorDim).Render("○")
		chRendered = lipgloss.NewStyle().Foreground(colorDim).Render(chStr)
		titleRendered = lipgloss.NewStyle().Foreground(colorMuted).Render(title)
	}
	return marker + " " + chRendered + " " + titleRendered
}

// truncateWidth 按"视觉宽度"截断（中文字符算 2 列），与 lipgloss.Width 同源。
// 不加省略号，供网格 cell 列对齐和 truncate 共用。
func truncateWidth(s string, maxW int) string {
	if lipgloss.Width(s) <= maxW {
		return s
	}
	var b strings.Builder
	cur := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if cur+rw > maxW {
			break
		}
		b.WriteRune(r)
		cur += rw
	}
	return b.String()
}

// renderDetailContent 构建右侧详情面板内容。
// 优先展示基础设定（大纲、角色），然后是运行时信息（提交、审阅等）。
func renderDetailContent(snap host.UISnapshot, contentW int) string {
	var b strings.Builder

	// 大纲
	if len(snap.Outline) > 0 {
		outlineHeader := ":: 大纲"
		if snap.Layered {
			outlineHeader = fmt.Sprintf(":: 完整卷弧大纲（%s）", snap.CurrentVolumeArc)
		}
		b.WriteString(panelTitleStyle.Render(outlineHeader))
		b.WriteString("\n")
		b.WriteString(renderOutlineSection(snap, contentW))
		compassStyle := lipgloss.NewStyle().Foreground(colorDim).Italic(true)
		if snap.Layered {
			if snap.NextVolumeTitle != "" {
				b.WriteString(compassStyle.Render("  ┄ 下一卷：" + snap.NextVolumeTitle))
				b.WriteString("\n")
			}
			if snap.HasSkeletonArcs {
				b.WriteString(compassStyle.Render(fmt.Sprintf("  ! 仍有 %d 个历史骨架弧待展开", snap.SkeletonArcCount)))
				b.WriteString("\n")
			}
			if snap.CompassDirection != "" {
				direction := fmt.Sprintf("  → 终局：%s", snap.CompassDirection)
				if snap.CompassScale != "" {
					direction += "（" + snap.CompassScale + "）"
				}
				b.WriteString(compassStyle.Render(truncate(direction, contentW)))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	// 角色
	if len(snap.Characters) > 0 {
		b.WriteString(panelTitleStyle.Render(":: 角色"))
		b.WriteString("\n")
		for _, c := range snap.Characters {
			writeBulletWrapped(&b, c, contentW, cardContentStyle)
		}
		b.WriteString("\n")
	}

	// 配角生态：累计已出场的次要角色总数 + 最近活跃前 5 名
	if snap.SupportingCount > 0 {
		b.WriteString(panelTitleStyle.Render(":: 配角生态"))
		b.WriteString("\n")
		b.WriteString(cardContentStyle.Render(truncate(fmt.Sprintf("已出场：%d 位", snap.SupportingCount), contentW)))
		b.WriteString("\n")
		for _, name := range snap.RecentSupporting {
			writeBulletWrapped(&b, name, contentW, cardContentStyle)
		}
		b.WriteString("\n")
	}

	if snap.Synopsis != "" {
		b.WriteString(panelTitleStyle.Render(":: 简介"))
		b.WriteString("\n")
		for _, line := range wrapStreamText(snap.Synopsis, contentW) {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render(line))
			b.WriteString("\n")
		}
		b.WriteString("\n\n")
	}

	// 前提
	if snap.Premise != "" {
		b.WriteString(panelTitleStyle.Render(":: 前提"))
		b.WriteString("\n")
		for _, line := range wrapStreamText(snap.Premise, contentW) {
			b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render(line))
			b.WriteString("\n")
		}
		b.WriteString("\n\n")
	}

	if snap.LastCommitSummary != "" {
		b.WriteString(cardTitleStyle.Render("~ 最近提交 ~"))
		b.WriteString("\n")
		writeWrapped(&b, snap.LastCommitSummary, contentW, cardContentStyle)
		b.WriteString("\n")
	}

	if snap.LastReviewSummary != "" {
		b.WriteString(cardTitleStyle.Render("~ 最近审阅 ~"))
		b.WriteString("\n")
		writeWrapped(&b, snap.LastReviewSummary, contentW, cardContentStyle)
		b.WriteString("\n")
	}

	if len(snap.RecentSummaries) > 0 {
		b.WriteString(cardTitleStyle.Render("~ 摘要 ~"))
		b.WriteString("\n")
		for _, s := range snap.RecentSummaries {
			writeWrapped(&b, s, contentW, cardContentStyle)
		}
	}

	return b.String()
}

// writeWrapped 按视觉宽度折行写入一段文本，每行独立渲染样式。
func writeWrapped(b *strings.Builder, text string, contentW int, style lipgloss.Style) {
	for _, line := range wrapStreamText(text, max(8, contentW)) {
		b.WriteString(style.Render(line))
		b.WriteString("\n")
	}
}

// writeBulletWrapped 写入一个"· "条目：按视觉宽度折行，续行以两列空格悬挂缩进。
func writeBulletWrapped(b *strings.Builder, text string, contentW int, style lipgloss.Style) {
	for i, line := range wrapStreamText(text, max(8, contentW-2)) {
		prefix := "· "
		if i > 0 {
			prefix = "  "
		}
		b.WriteString(style.Render(prefix + line))
		b.WriteString("\n")
	}
}
