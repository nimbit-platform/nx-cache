package web

import (
	"fmt"
	"time"

	"github.com/nimbit-platform/nx-cache/internal/store"
)

type LoginData struct {
	Error    string
	Username string
}

type DashboardData struct {
	Username     string
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

func NewDashboard(username, ttl, query, message string, page, pageSize int, stats store.Stats, entries []store.Entry, total int) DashboardData {
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
	return DashboardData{
		Username:     username,
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
