package reqlog

import (
	"context"
	"log/slog"
)

type ctxKey struct{}

func With(ctx context.Context, log *slog.Logger) context.Context {
	if log == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, log)
}

func From(ctx context.Context) *slog.Logger {
	return FromOr(ctx, nil)
}

func FromOr(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if ctx != nil {
		if log, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && log != nil {
			return log
		}
	}
	if fallback != nil {
		return fallback
	}
	return slog.Default()
}
