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
	Entries      []EntryView
	HitBarWidth  float64
	MissBarWidth float64
}

type EntryView struct {
	Hash       string
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
		Entries:      views,
		HitBarWidth:  hitW,
		MissBarWidth: missW,
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
