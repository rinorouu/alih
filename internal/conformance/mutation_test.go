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

package conformance

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"alih/internal/connector"
	"alih/internal/model"
	"alih/internal/snapshot"
)

// A conformance suite that cannot fail proves nothing. Each case below breaks
// one contract in the way a real connector author would break it, and asserts
// that the suite says so in words that name the mistake.
//
// The set is deliberately small: one violation per contract that a connector
// can plausibly get wrong, not one per assertion.
func TestTheSuiteCatchesEachContractViolation(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		subject  func() Subject
		contract func(TB, Subject)
		wants    []string
	}{
		{
			name: "an identity that cannot be a machine name",
			subject: func() Subject {
				s := referenceSubject()
				s.Connector = renamedConnector{s.Connector, "Example Provider"}
				return s
			},
			contract: AssertIdentity,
			wants:    []string{"not usable as a machine name"},
		},
		{
			name: "a normalizer that belongs to a different connector",
			subject: func() Subject {
				s := referenceSubject()
				s.Normalizer = misnamedNormalizer{s.Normalizer}
				return s
			},
			contract: AssertIdentity,
			wants:    []string{"normalizer says it belongs to connector", "must be the same string"},
		},
		{
			// The failure Phase 1 flagged: fail-closed is correct, but a
			// connector that needs authentication and forgot to declare gets
			// an unexplained upstream 401 instead of a reason.
			name: "authenticated attachments with no credential-host declaration",
			subject: func() Subject {
				s := referenceSubject()
				s.AttachmentIntent = AttachmentsAreAuthenticated
				return s
			},
			contract: AssertCredentialHosts,
			wants: []string{
				"expects authenticated attachment fetches",
				"does not implement connector.CredentialHostProvider",
				"401",
			},
		},
		{
			name: "a connector that never states how attachments are fetched",
			subject: func() Subject {
				s := referenceSubject()
				s.AttachmentIntent = AttachmentFetchingUnset
				return s
			},
			contract: AssertCredentialHosts,
			wants:    []string{"does not say how its attachments are fetched", "AttachmentsAreAuthenticated"},
		},
		{
			name: "credential hosts declared for anonymous attachments",
			subject: func() Subject {
				s := referenceSubject()
				s.Connector = hostDeclaringConnector{s.Connector, []string{"files.example.test"}}
				return s
			},
			contract: AssertCredentialHosts,
			wants:    []string{"fetched anonymously but declares credential hosts", "widens where the secret travels"},
		},
		{
			name: "a wildcard credential host",
			subject: func() Subject {
				s := referenceSubject()
				s.AttachmentIntent = AttachmentsAreAuthenticated
				s.Connector = hostDeclaringConnector{s.Connector, []string{"*.example.test"}}
				return s
			},
			contract: AssertCredentialHosts,
			wants:    []string{"looks like a wildcard", "no subdomain inheritance"},
		},
		{
			name: "a credential host written as a URL",
			subject: func() Subject {
				s := referenceSubject()
				s.AttachmentIntent = AttachmentsAreAuthenticated
				s.Connector = hostDeclaringConnector{s.Connector, []string{"https://files.example.test/attachments"}}
				return s
			},
			contract: AssertCredentialHosts,
			wants:    []string{"looks like a URL", "hostname only"},
		},
		{
			name: "field semantics that answer with an unknown state",
			subject: func() Subject {
				s := referenceSubject()
				s.FieldSemantics = brokenFieldSemantics{s.FieldSemantics.Connector()}
				return s
			},
			contract: AssertFieldSemantics,
			wants:    []string{"returned verdict", "UNPROVEN", "rather than guessing"},
		},
		{
			name: "an error Core cannot classify",
			subject: func() Subject {
				s := referenceSubject()
				s.SampleErrors = []error{errors.New("something went wrong upstream")}
				return s
			},
			contract: AssertOperationalErrors,
			wants:    []string{"carries no operational assessment", "never by matching its text"},
		},
		{
			name: "a portable model that renames the connector's own vocabulary",
			subject: func() Subject {
				s := referenceSubject()
				s.Normalizer = vocabularyCorruptingNormalizer{s.Normalizer}
				return s
			},
			contract: AssertArchivePipeline,
			wants:    []string{"another provider's word", "Use the word your source uses"},
		},
		{
			name: "an identity derived under the provider's own namespace",
			subject: func() Subject {
				s := referenceSubject()
				s.Normalizer = identityNamespaceBreakingNormalizer{s.Normalizer}
				return s
			},
			contract: AssertArchivePipeline,
			wants:    []string{"portable identifier Core derives", "fixed string"},
		},
		{
			name: "an observed value claiming semantics Alih never executed",
			subject: func() Subject {
				s := referenceSubject()
				s.Normalizer = semanticsBreakingNormalizer{s.Normalizer}
				return s
			},
			contract: AssertArchivePipeline,
			wants:    []string{"observed values are always", "OBSERVED_ONLY"},
		},
		{
			name: "a field definition missing an archived key",
			subject: func() Subject {
				s := referenceSubject()
				s.Normalizer = definitionKeyStrippingNormalizer{s.Normalizer}
				return s
			},
			contract: AssertArchivePipeline,
			wants:    []string{"contains no", "key"},
		},
		{
			name: "raw evidence pointing outside the sealed tree",
			subject: func() Subject {
				s := referenceSubject()
				s.Normalizer = rawPathBreakingNormalizer{s.Normalizer}
				return s
			},
			contract: AssertArchivePipeline,
			wants:    []string{"raw evidence path", "raw/"},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			messages := captureFailure(t, testCase.subject(), testCase.contract)
			if messages == "" {
				t.Fatal("the suite accepted a connector that violates this contract")
			}
			for _, want := range testCase.wants {
				if !strings.Contains(messages, want) {
					t.Errorf("the failure does not mention %q, so a connector author would not learn what to fix.\nGot: %s", want, messages)
				}
			}
		})
	}
}

// captureFailure runs one contract against a deliberately broken subject and
// returns what it reported, so a violation can be asserted on without failing
// the surrounding run.
func captureFailure(t *testing.T, subject Subject, contract func(TB, Subject)) string {
	t.Helper()
	recorder := &recordingTB{temp: t.TempDir()}
	func() {
		// A contract that calls Fatal stops, exactly as it would under a real
		// *testing.T; the sentinel panic is how that unwinds here.
		defer func() {
			if recovered := recover(); recovered != nil && recovered != errStopped {
				panic(recovered)
			}
		}()
		contract(recorder, subject)
	}()
	return recorder.String()
}

// recordingTB is a TB that remembers what a contract reported instead of
// failing the surrounding test.
type recordingTB struct {
	messages []string
	temp     string
	skipped  bool
}

const errStopped = "conformance: contract stopped"

func (r *recordingTB) Helper()               {}
func (r *recordingTB) TempDir() string       { return r.temp }
func (r *recordingTB) Error(args ...any)     { r.record(fmt.Sprint(args...)) }
func (r *recordingTB) Fatal(args ...any)     { r.record(fmt.Sprint(args...)); panic(errStopped) }
func (r *recordingTB) Skip(args ...any)      { r.skipped = true; panic(errStopped) }
func (r *recordingTB) record(message string) { r.messages = append(r.messages, message) }
func (r *recordingTB) String() string        { return strings.Join(r.messages, "\n") }

func (r *recordingTB) Errorf(format string, args ...any) { r.record(fmt.Sprintf(format, args...)) }
func (r *recordingTB) Fatalf(format string, args ...any) {
	r.record(fmt.Sprintf(format, args...))
	panic(errStopped)
}

// ---------------------------------------------------------------------------
// Deliberately broken adapters
// ---------------------------------------------------------------------------

type renamedConnector struct {
	connector.Connector
	name string
}

func (c renamedConnector) Name() string { return c.name }

type hostDeclaringConnector struct {
	connector.Connector
	hosts []string
}

func (c hostDeclaringConnector) CredentialHosts() []string { return c.hosts }

type misnamedNormalizer struct{ inner normalizerLike }

func (n misnamedNormalizer) Connector() string   { return "a-different-connector" }
func (n misnamedNormalizer) DisplayName() string { return n.inner.DisplayName() }
func (n misnamedNormalizer) NormalizeSnapshot(e snapshot.Evidence) (model.Archive, error) {
	return n.inner.NormalizeSnapshot(e)
}

type brokenFieldSemantics struct{ connectorName string }

func (s brokenFieldSemantics) Connector() string { return s.connectorName }
func (brokenFieldSemantics) ValidateFieldValue(string, []byte, []byte) (string, string) {
	return "PROBABLY_FINE", "this is not one of Core's verdicts"
}

// normalizerLike is the shape every wrapper below decorates.
type normalizerLike interface {
	Connector() string
	DisplayName() string
	NormalizeSnapshot(snapshot.Evidence) (model.Archive, error)
}

type vocabularyCorruptingNormalizer struct{ inner normalizerLike }

func (n vocabularyCorruptingNormalizer) Connector() string   { return n.inner.Connector() }
func (n vocabularyCorruptingNormalizer) DisplayName() string { return n.inner.DisplayName() }
func (n vocabularyCorruptingNormalizer) NormalizeSnapshot(e snapshot.Evidence) (model.Archive, error) {
	archive, err := n.inner.NormalizeSnapshot(e)
	if err != nil {
		return archive, err
	}
	// Rename this connector's containers into another provider's word.
	for index := range archive.Containers {
		archive.Containers[index].Kind = "spaces"
		archive.Containers[index].Source.Type = "spaces"
		archive.Containers[index].ID = model.PortableID(
			archive.Containers[index].Source.Provider, "spaces", archive.Containers[index].Source.ID)
	}
	return archive, nil
}

type identityNamespaceBreakingNormalizer struct{ inner normalizerLike }

func (n identityNamespaceBreakingNormalizer) Connector() string   { return n.inner.Connector() }
func (n identityNamespaceBreakingNormalizer) DisplayName() string { return n.inner.DisplayName() }
func (n identityNamespaceBreakingNormalizer) NormalizeSnapshot(e snapshot.Evidence) (model.Archive, error) {
	archive, err := n.inner.NormalizeSnapshot(e)
	if err != nil {
		return archive, err
	}
	for index := range archive.Identities {
		identity := &archive.Identities[index]
		identity.ID = model.PortableID(identity.Source.Provider, identity.Source.Type, identity.Source.ID)
	}
	return archive, nil
}

type semanticsBreakingNormalizer struct{ inner normalizerLike }

func (n semanticsBreakingNormalizer) Connector() string   { return n.inner.Connector() }
func (n semanticsBreakingNormalizer) DisplayName() string { return n.inner.DisplayName() }
func (n semanticsBreakingNormalizer) NormalizeSnapshot(e snapshot.Evidence) (model.Archive, error) {
	archive, err := n.inner.NormalizeSnapshot(e)
	if err != nil {
		return archive, err
	}
	for index := range archive.RecordFieldValues {
		archive.RecordFieldValues[index].SemanticsState = "SOURCE_DEFINITION_ONLY"
	}
	return archive, nil
}

type definitionKeyStrippingNormalizer struct{ inner normalizerLike }

func (n definitionKeyStrippingNormalizer) Connector() string   { return n.inner.Connector() }
func (n definitionKeyStrippingNormalizer) DisplayName() string { return n.inner.DisplayName() }
func (n definitionKeyStrippingNormalizer) NormalizeSnapshot(e snapshot.Evidence) (model.Archive, error) {
	archive, err := n.inner.NormalizeSnapshot(e)
	if err != nil {
		return archive, err
	}
	for index := range archive.FieldDefinitions {
		archive.FieldDefinitions[index].DefinitionJSON = []byte(`{"id":"fld-1"}`)
	}
	return archive, nil
}

type rawPathBreakingNormalizer struct{ inner normalizerLike }

func (n rawPathBreakingNormalizer) Connector() string   { return n.inner.Connector() }
func (n rawPathBreakingNormalizer) DisplayName() string { return n.inner.DisplayName() }
func (n rawPathBreakingNormalizer) NormalizeSnapshot(e snapshot.Evidence) (model.Archive, error) {
	archive, err := n.inner.NormalizeSnapshot(e)
	if err != nil {
		return archive, err
	}
	for index := range archive.Records {
		archive.Records[index].Source.RawPath = "../outside/000001.json"
	}
	return archive, nil
}

var _ = http.StatusOK
