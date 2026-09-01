// Copyright 2025 rinorouu
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

package organize

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const testID = "alih_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestComponentProducesPortableNames covers the naming rules one component must
// satisfy on every supported operating system.
func TestComponentProducesPortableNames(t *testing.T) {
	t.Parallel()

	suffix := "--0123456789abcdef"
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain name", "Release plan", "Release plan" + suffix},
		{"path separators", "reports/2026\\Q1", "reports_2026_Q1" + suffix},
		{"parent traversal", "../../etc", "_.._etc" + suffix},
		{"absolute path", "/etc/passwd", "_etc_passwd" + suffix},
		{"single dot", ".", "unnamed" + suffix},
		{"double dot", "..", "unnamed" + suffix},
		{"windows reserved", "CON", "_CON" + suffix},
		{"windows reserved with extension", "nul.txt", "_nul.txt" + suffix},
		{"windows separators and wildcards", `a<b>c:d"e|f?g*h`, "a_b_c_d_e_f_g_h" + suffix},
		{"control characters", "line\nbreak\ttab\x00null", "line_break_tab_null" + suffix},
		{"trailing dot and space", "report. ", "report" + suffix},
		{"empty", "", "unnamed" + suffix},
		{"whitespace only", "   ", "unnamed" + suffix},
		{"unicode is preserved", "Проект — Ünïcode 日本語", "Проект — Ünïcode 日本語" + suffix},
		{"invalid utf-8", "bad\xff\xfename", "unnamed" + suffix},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := component(testCase.input, testID)
			if got != testCase.want {
				t.Fatalf("component(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
			assertPortableComponent(t, got)
		})
	}
}

// TestComponentBoundsLength proves an arbitrarily long source name cannot
// produce a path component that a filesystem refuses, and that truncation never
// splits a rune.
func TestComponentBoundsLength(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		strings.Repeat("a", 4000),
		strings.Repeat("日", 4000),
		strings.Repeat("ünïcode ", 500),
	} {
		got := component(input, testID)
		if len(got) > 128 {
			t.Errorf("component is %d bytes for a %d byte name", len(got), len(input))
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncation split a rune: %q", got)
		}
		assertPortableComponent(t, got)
	}
}

// TestComponentKeepsDistinctSourcesDistinct proves names that a filesystem
// would otherwise merge stay separate, and that the same input is stable.
func TestComponentKeepsDistinctSourcesDistinct(t *testing.T) {
	t.Parallel()

	otherID := "alih_" + strings.Repeat("f", 64)
	if got := len(portableSuffix(testID)); got != suffixLength {
		t.Fatalf("suffix length = %d, want %d", got, suffixLength)
	}
	collisions := [][2]string{
		{"Report", "report"}, // case-insensitive filesystems
		{"Café", "Café"},    // Unicode composition
		{"a/b", "a_b"},       // sanitisation collision
		{"name ", "name"},    // trailing whitespace
		{strings.Repeat("x", 200), strings.Repeat("x", 300)}, // truncation collision
	}
	for _, pair := range collisions {
		first, second := component(pair[0], testID), component(pair[1], otherID)
		if strings.EqualFold(first, second) {
			t.Errorf("distinct sources %q and %q collided as %q", pair[0], pair[1], first)
		}
	}
	if component("Report", testID) != component("Report", testID) {
		t.Error("component is not stable for identical input")
	}
	// Two different names under the same portable identity cannot occur,
	// because a portable identity belongs to exactly one archived row.
	if component("Report", testID) == component("Report", otherID) {
		t.Error("the portable identity is not part of the component")
	}
}

// TestPortableSuffixIsAlwaysFilesystemSafe proves a hostile identifier is
// replaced by a digest rather than trusted as a path component.
func TestPortableSuffixIsAlwaysFilesystemSafe(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"portable identifier", "alih_ABC123", "abc123"},
		{"long portable identifier", testID, "0123456789abcdef"},
		{"empty", "", "unknown"},
		{"prefix only", "alih_", "unknown"},
		{"path traversal", "../../etc", ""},
		{"separator", "a/b", ""},
		{"control character", "a\nb", ""},
	}
	for _, testCase := range cases {
		got := portableSuffix(testCase.input)
		if testCase.want != "" && got != testCase.want {
			t.Errorf("portableSuffix(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
		if testCase.want == "" && !strings.HasPrefix(got, "id") {
			t.Errorf("portableSuffix(%q) = %q, want a digest", testCase.input, got)
		}
		assertPortableComponent(t, got)
	}
}

// TestAttachmentComponentKeepsAUsefulExtension proves an attachment stays
// openable without letting the filename decide the path.
func TestAttachmentComponentKeepsAUsefulExtension(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"ordinary file", "report.pdf", ".pdf"},
		{"no extension", "report", ""},
		{"traversal", "../../evil.sh", ".sh"},
		{"absurd extension", "file." + strings.Repeat("x", 40), ""},
		{"double extension", "archive.tar.gz", ".gz"},
	}
	for _, testCase := range cases {
		got := attachmentComponent(testCase.input, testID)
		if !strings.HasSuffix(got, testCase.want) {
			t.Errorf("attachmentComponent(%q) = %q, want suffix %q", testCase.input, got, testCase.want)
		}
		assertPortableComponent(t, got)
	}
}

// TestPluralKindNeverProducesAnEmptySegment proves a missing or hostile
// container kind still yields one safe directory name.
func TestPluralKindNeverProducesAnEmptySegment(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"space": "spaces", "Folder": "folders", "": "containers",
		"  ": "containers", "../..": "_s", "a/b": "a_bs",
	} {
		if got := pluralKind(input); got != want {
			t.Errorf("pluralKind(%q) = %q, want %q", input, got, want)
		}
		assertPortableComponent(t, pluralKind(input))
	}
}

// TestSafeRelativeRejectsEscapes proves the writer refuses any path that would
// leave the staged view.
func TestSafeRelativeRejectsEscapes(t *testing.T) {
	t.Parallel()

	for _, safe := range []string{"README.md", "a/b/c.md", "a/./b.md"} {
		if !safeRelative(safe) {
			t.Errorf("safeRelative(%q) = false", safe)
		}
	}
	for _, unsafe := range []string{"", ".", "..", "../escape", "a/../..", "/etc/passwd"} {
		if safeRelative(unsafe) {
			t.Errorf("safeRelative(%q) = true", unsafe)
		}
	}
}

// assertPortableComponent holds every generated path component to the rules
// that make it usable on Linux, macOS and Windows alike.
func assertPortableComponent(t *testing.T, component string) {
	t.Helper()
	switch {
	case component == "", component == ".", component == "..":
		t.Errorf("component %q is not a usable name", component)
	case strings.ContainsAny(component, `/\<>:"|?*`):
		t.Errorf("component %q contains a character a supported filesystem reserves", component)
	case strings.HasSuffix(component, " "), strings.HasSuffix(component, "."):
		t.Errorf("component %q ends in a character Windows silently trims", component)
	case !utf8.ValidString(component):
		t.Errorf("component %q is not valid UTF-8", component)
	}
	for _, character := range component {
		if character < 0x20 || character == 0x7f {
			t.Errorf("component %q contains a control character", component)
			return
		}
	}
}

// FuzzComponentIsAlwaysOnePortableName drives the sanitizer with arbitrary
// provider text. A table can only assert the cases somebody thought of; the
// invariant that matters is that no input whatsoever yields a component that
// escapes its directory or that a supported filesystem refuses.
func FuzzComponentIsAlwaysOnePortableName(f *testing.F) {
	for _, seed := range []string{
		"", ".", "..", "/", "CON", "nul.txt", "a/b", `c:\x`, "report. ",
		"\x00\x1b[31m", "日本語", "Ünïcode", strings.Repeat("x", 500), "\xff\xfe",
	} {
		f.Add(seed, "alih_0123456789abcdef")
		f.Add(seed, seed)
	}
	f.Fuzz(func(t *testing.T, name, portableID string) {
		got := component(name, portableID)
		assertPortableComponent(t, got)
		if len(got) > componentBudget+8 {
			t.Fatalf("component(%q, %q) is %d bytes", name, portableID, len(got))
		}
		// It must be exactly one path component on every supported platform.
		if strings.Contains(got, "/") || strings.Contains(got, `\`) {
			t.Fatalf("component(%q, %q) = %q spans directories", name, portableID, got)
		}
		// And it must be usable as a relative write target.
		if !safeRelative(got) {
			t.Fatalf("component(%q, %q) = %q is not a safe relative path", name, portableID, got)
		}
		// Naming is deterministic: the same inputs cannot drift between calls.
		if again := component(name, portableID); again != got {
			t.Fatalf("component is not deterministic: %q then %q", got, again)
		}
	})
}

// FuzzAttachmentComponentIsAlwaysPortable holds attachment filenames, which are
// the most directly provider-controlled names Alih writes, to the same rules.
func FuzzAttachmentComponentIsAlwaysPortable(f *testing.F) {
	for _, seed := range []string{"report.pdf", "../../evil.sh", ".bashrc", "a.tar.gz", "", "COM1.txt"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, filename string) {
		got := attachmentComponent(filename, testID)
		assertPortableComponent(t, got)
		if strings.Contains(got, "/") || strings.Contains(got, `\`) || !safeRelative(got) {
			t.Fatalf("attachmentComponent(%q) = %q is not one safe component", filename, got)
		}
	})
}

// FuzzSafeRelativeNeverAcceptsAnEscape is the last line of defence for every
// path the generator writes, so it is asserted directly rather than only
// through the callers that happen to use it today.
func FuzzSafeRelativeNeverAcceptsAnEscape(f *testing.F) {
	for _, seed := range []string{"a/b", "../x", "/x", "", ".", "..", "a/../../b", "./a"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, candidate string) {
		if !safeRelative(candidate) {
			return
		}
		cleaned := filepath.Clean(filepath.FromSlash(candidate))
		if filepath.IsAbs(cleaned) {
			t.Fatalf("safeRelative accepted the absolute path %q", candidate)
		}
		joined := filepath.Join("/staging", cleaned)
		if !strings.HasPrefix(joined, "/staging"+string(filepath.Separator)) && joined != "/staging" {
			t.Fatalf("safeRelative accepted %q, which escapes to %q", candidate, joined)
		}
	})
}
