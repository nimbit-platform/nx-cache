package applog

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
)

// ParseLevel maps LOG_LEVEL to a slog level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
}

// New returns a text or JSON slog logger writing to w.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:       level,
		AddSource:   level <= slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	}
	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}

func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.SourceKey {
		src, ok := a.Value.Any().(*slog.Source)
		if ok && src != nil {
			src.File = shortSource(src.File)
		}
		return a
	}
	if a.Value.Kind() == slog.KindAny {
		if err, ok := a.Value.Any().(error); ok && err != nil {
			return slog.String(a.Key, err.Error())
		}
	}
	return a
}

func shortSource(file string) string {
	dir, base := filepath.Split(filepath.ToSlash(file))
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		return base
	}
	return filepath.Base(dir) + "/" + base
}
