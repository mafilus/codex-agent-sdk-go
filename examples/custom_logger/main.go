// Package main demonstrates how to use a custom logging library (logrus) with the
// Codex SDK. Since the SDK expects *slog.Logger, this example shows how to create
// an adapter that bridges logrus to slog.
//
// This pattern works for any logging library (zap, zerolog, etc.) - just implement
// the slog.Handler interface.
package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// logrusHandler adapts logrus to the slog.Handler interface.
// This allows using logrus as the backend for slog-based logging.
type logrusHandler struct {
	logger *logrus.Logger
	attrs  []slog.Attr
	groups []string
}

// NewLogrusHandler creates a slog.Handler that writes to logrus.
func NewLogrusHandler(logger *logrus.Logger) slog.Handler {
	return &logrusHandler{
		logger: logger,
		attrs:  make([]slog.Attr, 0),
		groups: make([]string, 0),
	}
}

// Enabled reports whether the handler handles records at the given level.
func (h *logrusHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.logger.IsLevelEnabled(slogToLogrusLevel(level))
}

// Handle handles the Record by forwarding it to logrus.
func (h *logrusHandler) Handle(_ context.Context, record slog.Record) error {
	fields := make(logrus.Fields, record.NumAttrs()+len(h.attrs))

	// Add pre-configured attrs
	for _, attr := range h.attrs {
		key := h.buildKey(attr.Key)
		fields[key] = attr.Value.Any()
	}

	// Add record attrs
	record.Attrs(func(attr slog.Attr) bool {
		key := h.buildKey(attr.Key)
		fields[key] = attr.Value.Any()

		return true
	})

	entry := h.logger.WithFields(fields)
	level := slogToLogrusLevel(record.Level)

	entry.Log(level, record.Message)

	return nil
}

// WithAttrs returns a new Handler with the given attributes added.
func (h *logrusHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)

	return &logrusHandler{
		logger: h.logger,
		attrs:  newAttrs,
		groups: h.groups,
	}
}

// WithGroup returns a new Handler with the given group name prepended to
// the current group path.
func (h *logrusHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	newGroups := make([]string, len(h.groups)+1)
	copy(newGroups, h.groups)
	newGroups[len(h.groups)] = name

	return &logrusHandler{
		logger: h.logger,
		attrs:  h.attrs,
		groups: newGroups,
	}
}

// buildKey creates a field key with group prefix.
func (h *logrusHandler) buildKey(key string) string {
	if len(h.groups) == 0 {
		return key
	}

	var result strings.Builder
	for _, g := range h.groups {
		result.WriteString(g + ".")
	}

	return result.String() + key
}

// slogToLogrusLevel maps slog levels to logrus levels.
func slogToLogrusLevel(level slog.Level) logrus.Level {
	switch {
	case level >= slog.LevelError:
		return logrus.ErrorLevel
	case level >= slog.LevelWarn:
		return logrus.WarnLevel
	case level >= slog.LevelInfo:
		return logrus.InfoLevel
	default:
		return logrus.DebugLevel
	}
}

// displayMessage logs messages using logrus for unified output.
func displayMessage(log logrus.FieldLogger, msg codexsdk.Message) {
	switch m := msg.(type) {
	case *codexsdk.UserMessage:
		for _, block := range m.Content.Blocks() {
			if textBlock, ok := block.(*codexsdk.TextBlock); ok {
				log.WithField("role", "user").Info(textBlock.Text)
			}
		}

	case *codexsdk.AssistantMessage:
		for _, block := range m.Content {
			if textBlock, ok := block.(*codexsdk.TextBlock); ok {
				log.WithField("role", "assistant").Info(textBlock.Text)
			}
		}

	case *codexsdk.SystemMessage:
		log.WithField("role", "system").Debug("System message received")

	case *codexsdk.ResultMessage:
		fields := logrus.Fields{
			"component": "result",
		}

		if m.Usage != nil {
			fields["input_tokens"] = m.Usage.InputTokens
			fields["output_tokens"] = m.Usage.OutputTokens
		}

		log.WithFields(fields).Info("Query completed")
	}
}

func main() {
	// 1. Configure logrus with custom settings
	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})
	log.SetLevel(logrus.DebugLevel)

	log.Info("=== Custom Logger (Logrus) Example ===")

	// 2. Create slog.Logger with logrus adapter
	slogLogger := slog.New(NewLogrusHandler(log))

	// 3. Use with SDK
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log.Info("Running query with logrus-backed logger...")

	for msg, err := range codexsdk.Query(ctx, "What is 2+2? Answer in one short sentence.",
		codexsdk.WithLogger(slogLogger),
	) {
		if err != nil {
			log.WithError(err).Error("Query failed")

			return
		}

		displayMessage(log, msg)
	}

	log.Info("Example complete - all output above was logged through logrus")
}
