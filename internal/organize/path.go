// Copyright 2025 rinorouu
// Licensed under the Apache License, Version 2.0.

package organize

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var windowsReserved = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {}, "COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {}, "LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// componentBudget bounds one generated path component in bytes. Five named
// components plus the fixed segments keep the deepest generated path inside
// the 260-character limit Windows applies without long-path support, provided
// the user's own destination path is not itself unusually deep.
const componentBudget = 96

// component makes one platform-independent path component. Unicode is
// preserved without normalization; the stable ASCII portable-ID suffix makes
// case and normalization-equivalent source names non-colliding.
func component(name, portableID string) string {
	name = sanitizeName(name)
	suffix := portableSuffix(portableID)
	limit := componentBudget - len(suffix) - 2
	if limit < 0 {
		// Defensive: a portable identifier long enough to consume the whole
		// budget must still produce a component rather than a panic.
		limit = 0
	}
	name = truncateUTF8(name, limit)
	name = strings.TrimRight(name, " .")
	if name == "" {
		name = "unnamed"
	}
	base := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	if _, reserved := windowsReserved[base]; reserved {
		name = "_" + name
	}
	return name + "--" + suffix
}

func sanitizeName(input string) string {
	if !utf8.ValidString(input) {
		return "unnamed"
	}
	var output strings.Builder
	space := false
	for _, character := range strings.TrimSpace(input) {
		switch {
		case unicode.IsControl(character), strings.ContainsRune(`<>:"/\|?*`, character):
			character = '_'
		}
		if unicode.IsSpace(character) {
			if space {
				continue
			}
			character = ' '
			space = true
		} else {
			space = false
		}
		output.WriteRune(character)
	}
	return strings.Trim(output.String(), " .")
}

// suffixLength is how much of the portable identity each component carries. A
// prefix rather than a digest keeps a directory name visibly matched to the
// "Portable ID" the generated page states, while leaving most of the component
// budget for the readable source name. Distinct sources that shared a prefix
// and a name would fail closed on the exclusive create, never merge.
const suffixLength = 16

func portableSuffix(id string) string {
	trimmed := strings.TrimPrefix(strings.TrimSpace(id), "alih_")
	if trimmed == "" {
		return "unknown"
	}
	for _, character := range trimmed {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') {
			digest := sha256.Sum256([]byte(trimmed))
			trimmed = fmt.Sprintf("id%x", digest[:])
			break
		}
	}
	trimmed = strings.ToLower(trimmed)
	return truncateUTF8(trimmed, suffixLength)
}

func attachmentComponent(name, portableID string) string {
	name = sanitizeName(name)
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	if len(extension) > 16 {
		extension = ""
	}
	return component(base, portableID) + extension
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func (builder *viewBuilder) containerPath(value hierarchy) string {
	path := filepath.Join(builder.workspacePath, pluralKind(value.Container.Kind), component(pointerValue(value.Container.Name, value.Container.Kind), value.Container.ID))
	if value.ParentContainer != nil {
		path = filepath.Join(builder.workspacePath,
			pluralKind(value.ParentContainer.Kind), component(pointerValue(value.ParentContainer.Name, value.ParentContainer.Kind), value.ParentContainer.ID),
			pluralKind(value.Container.Kind), component(pointerValue(value.Container.Name, value.Container.Kind), value.Container.ID))
	}
	return path
}

func (builder *viewBuilder) collectionPath(value hierarchy) string {
	return filepath.Join(builder.containerPath(value), "collections", component(pointerValue(value.Collection.Name, "Collection"), value.Collection.ID))
}

func pluralKind(kind string) string {
	kind = sanitizeName(strings.ToLower(kind))
	if kind == "" || kind == "unnamed" {
		kind = "container"
	}
	return kind + "s"
}

func value(pointer *string, fallback string) string { return pointerValue(pointer, fallback) }

func pointerValue(pointer *string, fallback string) string {
	if pointer == nil || strings.TrimSpace(*pointer) == "" {
		return fallback
	}
	return *pointer
}
