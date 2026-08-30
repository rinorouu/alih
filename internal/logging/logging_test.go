// Copyright 2026 rinorouu
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
