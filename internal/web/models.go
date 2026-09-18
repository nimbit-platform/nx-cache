package web

import (
	"fmt"
	"strings"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/metrics"
	"github.com/nimbit-platform/nx-cache/internal/store"
)

type LoginData struct {
	Error    string
	Username string
}

type DashboardData struct {
	Username     string
	CSRFToken    string
	TTL          string
	Query        string
	Page         int
	Pages        int
	Total        int
	Message      string
	Stats        store.Stats
	Kinds        []store.KindCount
	Entries      []EntryView
	HitBarWidth  float64
	MissBarWidth float64
	Latency      metrics.Snapshot
	DayBars      []DayBar
	KindPieStyle string
	KindLegend   []KindLegend
}

type DayBar struct {
	Label     string
	Hits      int64
	Misses    int64
	HitWidth  float64
	MissWidth float64
}

type KindLegend struct {
	Kind       string
	Count      int64
	Percent    string
	ColorStyle string
}

type EntryView struct {
	Hash       string
	HashShort  string
	Label      string
	Kind       string
	Project    string
	Target     string
	Config     string
	Size       string
	Age        string
	Hits       int64
	LastAccess string
	Created    string
}

func NewDashboard(username, csrfToken, ttl, query, message string, page, pageSize int, stats store.Stats, entries []store.Entry, total int, latency metrics.Snapshot) DashboardData {
	if page < 1 {
		page = 1
	}
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	views := make([]EntryView, 0, len(entries))
	now := time.Now()
	for _, e := range entries {
		views = append(views, EntryView{
			Hash:       e.Hash,
			HashShort:  ShortHash(e.Hash),
			Label:      e.Label(),
			Kind:       e.Kind,
			Project:    e.Project,
			Target:     e.Target,
			Config:     e.Config,
			Size:       FormatBytes(e.Size),
			Age:        FormatAge(now.Sub(e.CreatedAt)),
			Hits:       e.Hits,
			LastAccess: FormatAge(now.Sub(e.LastAccessedAt)) + " ago",
			Created:    e.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"),
		})
	}
	hits := float64(stats.Hits)
	misses := float64(stats.Misses)
	sum := hits + misses
	var hitW, missW float64
	if sum == 0 {
		hitW, missW = 0, 0
	} else {
		hitW = hits / sum * 100
		missW = misses / sum * 100
	}
	dayBars := make([]DayBar, 0, len(stats.Days))
	var maxDay int64
	for _, day := range stats.Days {
		if total := day.Hits + day.Misses; total > maxDay {
			maxDay = total
		}
	}
	for _, day := range stats.Days {
		label := day.Day
		if parsed, err := time.Parse("2006-01-02", day.Day); err == nil {
			label = parsed.Format("Jan 02")
		}
		var hitWidth, missWidth float64
		if maxDay > 0 {
			hitWidth = float64(day.Hits) / float64(maxDay) * 100
			missWidth = float64(day.Misses) / float64(maxDay) * 100
		}
		dayBars = append(dayBars, DayBar{Label: label, Hits: day.Hits, Misses: day.Misses, HitWidth: hitWidth, MissWidth: missWidth})
	}

	var pieParts []string
	var kindTotal int64
	for _, kind := range stats.ByKind {
		kindTotal += kind.Count
	}
	legend := make([]KindLegend, 0, len(stats.ByKind))
	var pieStart float64
	for i, kind := range stats.ByKind {
		if kindTotal == 0 {
			break
		}
		pieEnd := pieStart + float64(kind.Count)/float64(kindTotal)*100
		color := kindPieColor(kind.Kind, i)
		pieParts = append(pieParts, fmt.Sprintf("%s %.2f%% %.2f%%", color, pieStart, pieEnd))
		legend = append(legend, KindLegend{
			Kind:       kind.Kind,
			Count:      kind.Count,
			Percent:    fmt.Sprintf("%.0f%%", float64(kind.Count)/float64(kindTotal)*100),
			ColorStyle: "background-color: " + color,
		})
		pieStart = pieEnd
	}
	pieStyle := "background: var(--muted)"
	if len(pieParts) > 0 {
		pieStyle = "background: conic-gradient(" + strings.Join(pieParts, ", ") + ")"
	}
	return DashboardData{
		Username:     username,
		CSRFToken:    csrfToken,
		TTL:          ttl,
		Query:        query,
		Page:         page,
		Pages:        pages,
		Total:        total,
		Message:      message,
		Stats:        stats,
		Kinds:        stats.ByKind,
		Entries:      views,
		HitBarWidth:  hitW,
		MissBarWidth: missW,
		Latency:      latency,
		DayBars:      dayBars,
		KindPieStyle: pieStyle,
		KindLegend:   legend,
	}
}

func kindPieColor(kind string, index int) string {
	switch kind {
	case "build":
		return "oklch(69.6% .17 162.48)"
	case "test":
		return "oklch(68.5% .169 237.323)"
	case "lint":
		return "oklch(76.9% .188 70.08)"
	case "e2e":
		return "oklch(60.6% .25 292.717)"
	case "typecheck":
		return "oklch(71.5% .143 215.221)"
	case "serve":
		return "oklch(65% .2 330)"
	case "unknown", "":
		return "oklch(55.6% 0 0)"
	default:
		return []string{
			"oklch(68.5% .169 237.323)",
			"oklch(76.9% .188 70.08)",
			"oklch(60.6% .25 292.717)",
			"oklch(65% .2 330)",
		}[index%4]
	}
}

func ShortHash(hash string) string {
	if len(hash) <= 16 {
		return hash
	}
	return hash[:12] + "…"
}

func KindBadgeClass(kind string) string {
	switch kind {
	case "build":
		return "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400"
	case "test":
		return "border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-400"
	case "lint":
		return "border-amber-500/30 bg-amber-500/10 text-amber-800 dark:text-amber-400"
	case "e2e":
		return "border-violet-500/30 bg-violet-500/10 text-violet-700 dark:text-violet-400"
	case "typecheck":
		return "border-cyan-500/30 bg-cyan-500/10 text-cyan-700 dark:text-cyan-400"
	case "serve":
		return "border-fuchsia-500/30 bg-fuchsia-500/10 text-fuchsia-700 dark:text-fuchsia-400"
	default:
		return ""
	}
}

func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for val := n / unit; val >= unit; val /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func FormatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, h)
}

func FormatRate(stats store.Stats) string {
	return fmt.Sprintf("%.1f%%", stats.HitRate())
}

func FormatLatency(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	if d < time.Millisecond {
		return "<1 ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%d ms", d/time.Millisecond)
	}
	return fmt.Sprintf("%.2f s", d.Seconds())
}

func FormatLatencyPair(stats metrics.OperationStats) string {
	if stats.Count == 0 {
		return "No data"
	}
	return FormatLatency(stats.P50) + " / " + FormatLatency(stats.P95)
}

func LatencyHint(operation string, stats metrics.OperationStats) string {
	if stats.Count == 0 {
		return "No " + operation + "s since startup"
	}
	return fmt.Sprintf("%d %ss · %s · %d samples", stats.Count, operation, FormatBytes(stats.Bytes), stats.Samples)
}
