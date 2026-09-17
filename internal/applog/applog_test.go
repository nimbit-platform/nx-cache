package applog

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"DEBUG":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Fatalf("%q: got %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Fatal("expected invalid level")
	}
}

func TestJSONLoggerSerializesErrors(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := New(&buf, slog.LevelInfo, "json")
	log.Error("failed", "err", errors.New("no such key"))
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["msg"] != "failed" {
		t.Fatalf("msg=%v", rec["msg"])
	}
	if rec["err"] != "no such key" {
		t.Fatalf("err=%v body=%s", rec["err"], buf.String())
	}
}

func TestTextLoggerRespectsLevel(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := New(&buf, slog.LevelWarn, "text")
	log.Info("quiet")
	log.Warn("loud")
	out := buf.String()
	if strings.Contains(out, "quiet") {
		t.Fatalf("info should be filtered: %s", out)
	}
	if !strings.Contains(out, "loud") {
		t.Fatalf("warn missing: %s", out)
	}
}

func TestShortSource(t *testing.T) {
	t.Parallel()
	if got := shortSource("/tmp/internal/cacheapi/handler.go"); got != "cacheapi/handler.go" {
		t.Fatalf("got %s", got)
	}
}
