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

package clickup

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"alih/internal/connector"
	"alih/internal/snapshot"
)

func TestNormalizeSnapshotProducesDeterministicPortableModelWithSourceIDs(t *testing.T) {
	t.Parallel()

	fixture := &scanFixture{t: t, token: "portable-secret"}
	client := fixtureClient(t, fixture.roundTrip)
	workspace := connector.Workspace{ID: "w1", Name: "Portable Test"}
	target := filepath.Join(t.TempDir(), "m3")
	session, err := snapshot.Begin(target, "clickup", workspace, connector.Identity{ID: "u1", Name: "Portable User"}, fixture.token)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Extract(context.Background(), fixture.token, workspace, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Complete(result); err != nil {
		t.Fatal(err)
	}
	evidence, err := snapshot.LoadComplete(target)
	if err != nil {
		t.Fatal(err)
	}

	first, err := NormalizeSnapshot(evidence)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeSnapshot(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same raw evidence produced different portable models")
	}
	if len(first.Containers) != 2 || len(first.Collections) != 2 || len(first.Records) != 3 ||
		len(first.Comments) != 4 || len(first.Attachments) != 2 || len(first.FieldDefinitions) != 4 || len(first.Relationships) != 2 {
		t.Fatalf("portable counts: containers=%d collections=%d records=%d comments=%d attachments=%d fields=%d relationships=%d",
			len(first.Containers), len(first.Collections), len(first.Records), len(first.Comments), len(first.Attachments), len(first.FieldDefinitions), len(first.Relationships))
	}

	wantSourceIDs := map[string]bool{"t1": true, "t2": true, "t3": true}
	for _, record := range first.Records {
		delete(wantSourceIDs, record.Source.ID)
		if record.Source.Provider != "clickup" || record.Source.Type != "task" || !strings.HasPrefix(record.Source.RawPath, "raw/raw/") {
			t.Fatalf("record source metadata = %#v", record.Source)
		}
		if record.Title == nil || *record.Title == "" {
			t.Fatalf("record %s lost its title", record.Source.ID)
		}
	}
	if len(wantSourceIDs) != 0 {
		t.Fatalf("missing original Task IDs: %#v", wantSourceIDs)
	}

	wantRelationships := map[string]bool{"t1->t2": true, "t1<->t3": true}
	for _, relationship := range first.Relationships {
		delete(wantRelationships, relationship.Source.ID)
		if !relationship.Source.IDComposite || relationship.FromRecordID == nil || relationship.ToRecordID == nil || relationship.ResolutionState != "RESOLVED" {
			t.Fatalf("relationship = %#v", relationship)
		}
	}
	if len(wantRelationships) != 0 {
		t.Fatalf("missing relationships: %#v", wantRelationships)
	}
	for _, attachment := range first.Attachments {
		if attachment.Source.ID == "" || attachment.DownloadStatus != "UNRESOLVED" || attachment.Error == nil {
			t.Fatalf("attachment without fixture URL was not explicitly unresolved: %#v", attachment)
		}
	}
}
