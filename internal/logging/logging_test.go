package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestNewHonorsLogLevel(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := New(&output, slog.LevelInfo)
	logger.DebugContext(context.Background(), "hidden")
	logger.InfoContext(context.Background(), "visible")

	if strings.Contains(output.String(), "hidden") {
		t.Fatalf("debug log was emitted at info level: %q", output.String())
	}
	if !strings.Contains(output.String(), "visible") {
		t.Fatalf("info log was not emitted: %q", output.String())
	}
}
