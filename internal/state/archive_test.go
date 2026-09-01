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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sealedArchive(t *testing.T, content string) (string, ArchiveIdentity) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, ManifestFilename), []byte(content), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	checksum, err := ManifestChecksum(path)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	return path, ArchiveIdentity{
		Path: path, ManifestChecksum: checksum, LogicalDigest: testDigestB,
		ArchiveStatus: "CREATED_UNVERIFIED",
	}
}

func TestManifestChecksumIdentifiesTheSealedBytes(t *testing.T) {
	t.Parallel()
	path, identity := sealedArchive(t, `{"schema_version":4}`)
	again, err := ManifestChecksum(path)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if again != identity.ManifestChecksum {
		t.Fatal("the checksum of unchanged bytes changed")
	}
	if err := validateDigest("manifest checksum", identity.ManifestChecksum); err != nil {
		t.Fatalf("checksum is not a storable digest: %v", err)
	}
	// Formatting is part of identity: a resealed manifest is a different archive.
	if err := os.WriteFile(filepath.Join(path, ManifestFilename), []byte(`{"schema_version": 4}`), 0o600); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	reformatted, err := ManifestChecksum(path)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if reformatted == identity.ManifestChecksum {
		t.Fatal("reformatted manifest kept the same identity")
	}
}

func TestInspectArchiveNeverUpgradesAWeakenedClaim(t *testing.T) {
	t.Parallel()

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		_, identity := sealedArchive(t, `{"schema_version":4}`)
		if condition := InspectArchive(identity); condition != ArchivePresent {
			t.Fatalf("condition = %s, want PRESENT", condition)
		}
	})

	t.Run("changed", func(t *testing.T) {
		t.Parallel()
		path, identity := sealedArchive(t, `{"schema_version":4}`)
		if err := os.WriteFile(filepath.Join(path, ManifestFilename), []byte(`{"schema_version":4,"x":1}`), 0o600); err != nil {
			t.Fatalf("rewrite manifest: %v", err)
		}
		if condition := InspectArchive(identity); condition != ArchiveChanged {
			t.Fatalf("condition = %s, want CHANGED", condition)
		}
	})

	t.Run("moved away", func(t *testing.T) {
		t.Parallel()
		path, identity := sealedArchive(t, `{"schema_version":4}`)
		if err := os.Rename(path, path+"-elsewhere"); err != nil {
			t.Fatalf("move archive: %v", err)
		}
		if condition := InspectArchive(identity); condition != ArchiveMissing {
			t.Fatalf("condition = %s, want MISSING", condition)
		}
	})

	t.Run("manifest removed", func(t *testing.T) {
		t.Parallel()
		path, identity := sealedArchive(t, `{"schema_version":4}`)
		if err := os.Remove(filepath.Join(path, ManifestFilename)); err != nil {
			t.Fatalf("remove manifest: %v", err)
		}
		if condition := InspectArchive(identity); condition != ArchiveMissing {
			t.Fatalf("condition = %s, want MISSING", condition)
		}
	})

	t.Run("not a directory", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "archive")
		if err := os.WriteFile(path, []byte("not an archive"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		condition := InspectArchive(ArchiveIdentity{Path: path, ManifestChecksum: testDigest})
		if condition != ArchiveUnreadable {
			t.Fatalf("condition = %s, want UNREADABLE", condition)
		}
	})

	t.Run("no path recorded", func(t *testing.T) {
		t.Parallel()
		if condition := InspectArchive(ArchiveIdentity{}); condition != ArchiveUnreadable {
			t.Fatalf("condition = %s, want UNREADABLE", condition)
		}
	})
}

func TestSafeLimitationsKeepARecordWritable(t *testing.T) {
	t.Parallel()
	limitations := make([]string, 0, maxLimitations+10)
	for index := 0; index < maxLimitations+10; index++ {
		limitations = append(limitations, "limitation")
	}
	safe := SafeLimitations(limitations)
	if len(safe) != maxLimitations {
		t.Fatalf("kept %d limitations, want %d", len(safe), maxLimitations)
	}
	if !strings.Contains(safe[len(safe)-1], "further limitations") {
		t.Fatalf("dropped limitations were not declared: %q", safe[len(safe)-1])
	}

	dangerous := SafeLimitations([]string{
		"attachment \x07content\nunavailable", "   ", strings.Repeat("x", maxTextBytes*2),
		string([]byte{0xff, 0xfe}) + "invalid encoding",
	})
	if len(dangerous) != 3 {
		t.Fatalf("sanitized limitations = %#v", dangerous)
	}
	for _, limitation := range dangerous {
		if err := validateText("limitation", limitation, maxTextBytes, false); err != nil {
			t.Fatalf("sanitized limitation is still unstorable: %v (%q)", err, limitation)
		}
	}
	if !strings.HasSuffix(dangerous[1], "...") {
		t.Fatalf("an over-long limitation was not marked as truncated: %q", dangerous[1])
	}
}
