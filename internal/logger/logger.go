package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	reset   = "\x1b[0m"
	red     = "\x1b[31m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	magenta = "\x1b[35m"
	cyan    = "\x1b[36m"
)

type Options struct {
	Level  slog.Level
	Prefix string // shown as [Prefix] at the start of each line, e.g. "Worker"
	Color  bool
}

type Handler struct {
	w      io.Writer
	level  slog.Level
	prefix string
	pid    int
	color  bool
	attrs  []slog.Attr

	mu   *sync.Mutex
	last *time.Time
}

func New(w io.Writer, opts Options) *Handler {
	if opts.Prefix == "" {
		opts.Prefix = "App"
	}
	return &Handler{
		w:      w,
		level:  opts.Level,
		prefix: opts.Prefix,
		pid:    os.Getpid(),
		color:  opts.Color,
		mu:     &sync.Mutex{},
		last:   new(time.Time),
	}
}

func (h *Handler) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= h.level
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var delta time.Duration
	if !h.last.IsZero() {
		delta = r.Time.Sub(*h.last)
	}
	*h.last = r.Time

	levelText, levelColor := levelMeta(r.Level)
	ts := r.Time.Format("01/02/2006, 3:04:05 PM")

	ctxLabel := ""
	extras := make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs())
	for _, a := range h.attrs {
		if a.Key == "context" {
			ctxLabel = a.Value.String()
			continue
		}
		extras = append(extras, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "context" {
			ctxLabel = a.Value.String()
			return true
		}
		extras = append(extras, a)
		return true
	})

	wrap := func(c, s string) string {
		if !h.color {
			return s
		}
		return c + s + reset
	}

	var sb strings.Builder
	sb.WriteString(wrap(levelColor, fmt.Sprintf("[%s] %d ", h.prefix, h.pid)))
	sb.WriteString(" - ")
	sb.WriteString(ts)
	sb.WriteString("     ")
	sb.WriteString(wrap(levelColor, fmt.Sprintf("%-5s", levelText)))
	if ctxLabel != "" {
		sb.WriteString(" ")
		sb.WriteString(wrap(yellow, "["+ctxLabel+"]"))
	}
	sb.WriteString(" ")
	sb.WriteString(wrap(levelColor, r.Message))

	for _, a := range extras {
		sb.WriteString(" ")
		fmt.Fprintf(&sb, "%s=%v", a.Key, a.Value)
	}

	if delta > 0 && delta < time.Hour {
		sb.WriteString(" ")
		sb.WriteString(wrap(yellow, fmt.Sprintf("+%dms", delta.Milliseconds())))
	}
	sb.WriteString("\n")

	_, err := io.WriteString(h.w, sb.String())
	return err
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *Handler) WithGroup(_ string) slog.Handler {
	return h
}

func levelMeta(l slog.Level) (string, string) {
	switch {
	case l >= slog.LevelError:
		return "ERROR", red
	case l >= slog.LevelWarn:
		return "WARN", yellow
	case l >= slog.LevelInfo:
		return "LOG", green
	case l >= slog.LevelDebug:
		return "DEBUG", magenta
	default:
		return "VERB", cyan
	}
}

func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func Ctx(l *slog.Logger, name string) *slog.Logger {
	return l.With(slog.String("context", name))
}
