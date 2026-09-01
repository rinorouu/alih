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

package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ManifestFilename is the sealed file whose bytes identify an archive. A
// manifest cannot contain its own checksum, which is exactly why the checksum
// of those bytes is a usable external identity for the archive.
const ManifestFilename = "manifest.json"

// ArchiveCondition is what a recorded verification claim is worth right now.
// Only PRESENT means the archive that was verified is still the archive on disk.
type ArchiveCondition string

const (
	ArchivePresent    ArchiveCondition = "PRESENT"
	ArchiveMissing    ArchiveCondition = "MISSING"
	ArchiveUnreadable ArchiveCondition = "UNREADABLE"
	ArchiveChanged    ArchiveCondition = "CHANGED"
)

// ManifestChecksum returns the digest of the archive manifest exactly as it was
// sealed. It reads the bytes rather than any parsed representation, so a change
// in formatting is a change in identity.
func ManifestChecksum(archivePath string) (string, error) {
	manifest := filepath.Join(archivePath, ManifestFilename)
	info, err := os.Lstat(manifest)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", manifest)
	}
	file, err := os.Open(manifest)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// InspectArchive reports whether the archive a verification claim points at is
// still there and still the same. It never modifies the archive, and it never
// upgrades an unreadable or changed archive back into a proven one.
func InspectArchive(identity ArchiveIdentity) ArchiveCondition {
	if strings.TrimSpace(identity.Path) == "" {
		return ArchiveUnreadable
	}
	info, err := os.Lstat(identity.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return ArchiveMissing
	}
	if err != nil || !info.IsDir() {
		return ArchiveUnreadable
	}
	checksum, err := ManifestChecksum(identity.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return ArchiveMissing
	}
	if err != nil {
		return ArchiveUnreadable
	}
	if checksum != identity.ManifestChecksum {
		return ArchiveChanged
	}
	return ArchivePresent
}

// SafeLimitations bounds recorded limitations so that one archive with many
// findings can never make the state file itself unwritable. Dropped entries are
// declared rather than silently discarded, because a limitation that vanishes
// would understate what the archive says about itself.
func SafeLimitations(limitations []string) []string {
	safe := make([]string, 0, len(limitations))
	for _, limitation := range limitations {
		cleaned := sanitizeText(limitation)
		if cleaned == "" {
			continue
		}
		safe = append(safe, cleaned)
	}
	if len(safe) <= maxLimitations {
		return safe
	}
	kept := append([]string(nil), safe[:maxLimitations-1]...)
	return append(kept, fmt.Sprintf(
		"%d further limitations are recorded in the archive and its recovery report.",
		len(safe)-(maxLimitations-1)))
}

// sanitizeText makes provider-influenced text safe to store: control characters
// cannot reach a terminal, invalid encoding cannot corrupt the record, and the
// value cannot exceed the schema's bound.
func sanitizeText(value string) string {
	cleaned := strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, strings.ToValidUTF8(value, ""))
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) <= maxTextBytes {
		return cleaned
	}
	truncated := cleaned[:maxTextBytes-3]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "..."
}
