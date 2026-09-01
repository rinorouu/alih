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
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"alih/internal/archive"
	"alih/internal/connector"
	"alih/internal/credentials"
	"alih/internal/exporter"
	"alih/internal/model"
	"alih/internal/organize"
	"alih/internal/report"
	"alih/internal/snapshot"
	"alih/internal/verify"
)

// TB is the part of *testing.T the contracts use.
//
// It exists so the contracts can be exercised by the suite's own tests: a
// conformance check that cannot itself be shown to fail is not evidence. Every
// *testing.T satisfies it, so a connector author passes their own t and never
// sees this type.
type TB interface {
	Helper()
	Error(args ...any)
	Errorf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Skip(args ...any)
	TempDir() string
}

// AttachmentFetching is what a connector intends to happen when Alih downloads
// an archived attachment.
//
// It exists because Core cannot tell the two intentions apart, and the
// difference decides whether a missing credential-host declaration is correct
// or is the bug that produced an unexplained upstream 401. Declaring the
// intention in the test fixture keeps the production contract fail-closed and
// optional: nothing about a connector's shipped behaviour changes, and the
// suite gains the one fact it needs to hold the connector to its own intent.
type AttachmentFetching int

const (
	// AttachmentFetchingUnset is the zero value. The suite refuses to guess,
	// because guessing is exactly what produces the silent failure.
	AttachmentFetchingUnset AttachmentFetching = iota
	// AttachmentsAreAnonymous states that archived attachment URLs carry their
	// own authorisation — a pre-signed link, or a public object — so Alih must
	// send no credential. A connector declaring this must declare no hosts.
	AttachmentsAreAnonymous
	// AttachmentsAreAuthenticated states that the provider expects Alih's
	// credential on the attachment request. A connector declaring this must
	// implement connector.CredentialHostProvider and name the hosts.
	AttachmentsAreAuthenticated
	// AttachmentsAreNotSupported states the connector archives no attachments.
	AttachmentsAreNotSupported
)

// Subject is the connector under test, plus the few facts about it that Core
// cannot discover on its own.
//
// Only Connector and Intent are required. Everything else is optional, and the
// suite runs the contracts the subject supplies enough information for rather
// than demanding a connector implement surfaces it does not have. What is
// skipped is reported, so a passing run never quietly means "nothing ran".
type Subject struct {
	// Connector is the adapter. It must satisfy connector.Connector; the suite
	// type-asserts the richer interfaces and skips what is absent.
	Connector connector.Connector

	// AttachmentIntent states what the connector expects to happen when Alih
	// fetches an archived attachment. Required: see AttachmentFetching.
	AttachmentIntent AttachmentFetching

	// Normalizer turns this connector's raw evidence into the portable model.
	// Without it the archive contracts cannot run.
	Normalizer exporter.Normalizer

	// FieldSemantics interprets archived custom-field evidence. Optional: a
	// connector without it archives field values that verification leaves
	// unproven rather than rejects.
	FieldSemantics verify.FieldSemantics

	// Credential is a fake secret used to drive the live pipeline. It must not
	// be a real credential. The suite asserts it never escapes into an
	// artifact, so it has to be a value that would be recognisable if it did.
	Credential string

	// HTTPClient serves attachment requests during the archive contract. A
	// subject that supplies none skips the pipeline contracts rather than
	// reaching the network.
	HTTPClient *http.Client

	// SampleErrors are representative failures this connector returns, one per
	// situation it can actually produce. The suite proves each carries a stable
	// Core reason, which is what lets Alih report a rejected credential
	// differently from an unreachable provider without ever reading a
	// provider's error text. A connector that supplies none skips the contract.
	SampleErrors []error
}

// Run executes every contract the subject supplies enough information for.
//
// It is the entry point a connector's own test calls. Each contract is a
// subtest named after the contract rather than after an implementation detail,
// so a failure reads as the rule that was broken.
func Run(t *testing.T, subject Subject) {
	t.Helper()
	if subject.Connector == nil {
		t.Fatal("conformance: Subject.Connector is nil; the suite has nothing to test")
	}

	t.Run("identity", func(t *testing.T) { AssertIdentity(t, subject) })
	t.Run("capabilities", func(t *testing.T) { AssertCapabilities(t, subject) })
	t.Run("credential_scoping", func(t *testing.T) { AssertCredentialScoping(t, subject) })
	t.Run("credential_hosts", func(t *testing.T) { AssertCredentialHosts(t, subject) })
	t.Run("field_semantics", func(t *testing.T) { AssertFieldSemantics(t, subject) })
	t.Run("operational_errors", func(t *testing.T) { AssertOperationalErrors(t, subject) })
	t.Run("archive_pipeline", func(t *testing.T) { AssertArchivePipeline(t, subject) })
}

// AssertIdentity holds a connector to the identity contract: one stable machine
// name that Core can use as a key, and a human name that never has to be
// invented by Core.
func AssertIdentity(t TB, subject Subject) {
	t.Helper()

	name := subject.Connector.Name()
	if err := credentials.ValidateConnector(name); err != nil {
		t.Fatalf("connector identity %q is not usable as a machine name: %v\n"+
			"Name() selects this connector's credential, derives its environment variable and is sealed into archives. "+
			"Use lowercase letters, digits, - and _.", name, err)
	}
	if second := subject.Connector.Name(); second != name {
		t.Errorf("Name() returned %q then %q; connector identity must be stable, because archives written under one name are looked up under it later", name, second)
	}

	// A display name is optional, but if one is offered it must be usable.
	if namer, ok := subject.Connector.(interface{ DisplayName() string }); ok {
		display := namer.DisplayName()
		if strings.TrimSpace(display) == "" {
			t.Errorf("DisplayName() is empty; either return a human name for this provider or do not implement DisplayName, so Core falls back to %q", name)
		}
	}
	if subject.Normalizer != nil {
		if got := subject.Normalizer.Connector(); got != name {
			t.Errorf("the normalizer says it belongs to connector %q but the adapter is named %q; "+
				"Core selects a normalizer by the name an archive records, so these must be the same string", got, name)
		}
	}
	if subject.FieldSemantics != nil {
		if got := subject.FieldSemantics.Connector(); got != name {
			t.Errorf("field semantics say they belong to connector %q but the adapter is named %q; "+
				"Core selects an interpreter by the name an archive records, so these must be the same string", got, name)
		}
	}
}

// AssertCapabilities holds a connector to the capability contract. Capabilities
// are how a connector says what it can establish without turning an unknown
// into a claim, so they must be deterministic and valid on their own terms.
func AssertCapabilities(t TB, subject Subject) {
	t.Helper()

	provider, ok := subject.Connector.(connector.CapabilityProvider)
	if !ok {
		t.Skip("connector declares no capability contract; Core will treat it as having made no capability claims")
	}
	first := provider.CapabilityContract()
	second := provider.CapabilityContract()

	if first.SchemaVersion != connector.CapabilitySchemaVersion {
		t.Errorf("capability contract declares schema version %d, but this build of Alih speaks version %d",
			first.SchemaVersion, connector.CapabilitySchemaVersion)
	}
	if len(first.Capabilities) == 0 {
		t.Error("the capability contract is empty; a connector that establishes nothing cannot be reconciled against an archive")
	}
	if !sameCapabilities(first.Capabilities, second.Capabilities) {
		t.Error("CapabilityContract() returned different declarations on two calls; " +
			"the declaration is what a connector implements, not what one run observed, so it must not vary")
	}

	// Core's own validator is the authority on what a contract must look like;
	// repeating its rules here would let the two drift apart.
	if err := connector.ValidateCapabilityContract(first); err != nil {
		t.Errorf("the capability contract is not valid: %v\n"+
			"Every capability needs a known identifier, a REQUIRED or OPTIONAL requirement, and an implementation state.", err)
	}

	var required int
	for _, capability := range first.Capabilities {
		switch capability.Requirement {
		case connector.CapabilityRequired:
			required++
		case connector.CapabilityOptional:
		default:
		}
	}
	if required == 0 {
		t.Error("no capability is declared required; at least one capability must be required, or no run could ever be judged incomplete")
	}
}

// AssertCredentialScoping proves this connector's credential is addressed by
// its own name and cannot be reached, replaced, or leaked through another's.
func AssertCredentialScoping(t TB, subject Subject) {
	t.Helper()

	name := subject.Connector.Name()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store := credentials.NewFileStore(filepath.Join(directory, "credentials.json"))

	const secret = "conformance-fake-secret-not-a-real-credential"
	if err := store.Save(name, secret); err != nil {
		t.Fatalf("this connector's name cannot store a credential: %v", err)
	}
	loaded, err := store.Load(name)
	if err != nil || loaded != secret {
		t.Fatalf("credential round trip returned %q, %v", loaded, err)
	}

	// Another connector's slot must be untouched and unreadable from here.
	const other = "some-other-connector"
	if other != name {
		if _, err := store.Load(other); err == nil {
			t.Error("a credential saved for this connector was readable under another connector's name")
		}
		if err := store.Save(other, "another-fake-secret"); err != nil {
			t.Fatalf("saving an unrelated connector failed: %v", err)
		}
		if kept, err := store.Load(name); err != nil || kept != secret {
			t.Errorf("saving another connector's credential disturbed this one: %q, %v", kept, err)
		}
	}
}

// AssertCredentialHosts is the contract that makes a fail-closed default safe
// to live with.
//
// connector.CredentialHostProvider is optional, and a connector that does not
// implement it gets no credential attached to any attachment request. That is
// correct for pre-signed links and wrong for a provider that expects
// authentication — and the second case fails as an unexplained upstream 401
// with nothing pointing at the cause. The subject states its intent, and this
// contract holds the connector to it.
func AssertCredentialHosts(t TB, subject Subject) {
	t.Helper()

	name := subject.Connector.Name()
	var declared []string
	provider, implements := credentialHostProvider(subject)
	if implements {
		declared = provider.CredentialHosts()
	}

	switch subject.AttachmentIntent {
	case AttachmentFetchingUnset:
		t.Fatalf("connector %q does not say how its attachments are fetched.\n"+
			"Set Subject.AttachmentIntent to one of:\n"+
			"  AttachmentsAreAnonymous      - attachment URLs carry their own authorisation (pre-signed or public)\n"+
			"  AttachmentsAreAuthenticated  - the provider expects Alih's credential on the attachment request\n"+
			"  AttachmentsAreNotSupported   - this connector archives no attachments\n"+
			"Alih sends no credential unless a connector declares its hosts, so this is the one fact the suite cannot infer.", name)

	case AttachmentsAreAuthenticated:
		if !implements {
			t.Fatalf("connector %q expects authenticated attachment fetches but does not implement connector.CredentialHostProvider.\n"+
				"Alih is fail-closed: without a declaration it sends no credential, so every attachment download will be anonymous "+
				"and the provider will answer 401 or 403 with nothing explaining why.\n"+
				"Implement CredentialHosts() on the adapter and return the exact hostnames that may receive this connector's credential.", name)
		}
		if len(declared) == 0 {
			t.Fatalf("connector %q expects authenticated attachment fetches but CredentialHosts() returned nothing.\n"+
				"An empty declaration means the same as no declaration: the credential is attached to no host. "+
				"Return the exact hostnames the provider serves attachments from.", name)
		}
		for _, host := range declared {
			assertUsableHost(t, name, host)
		}

	case AttachmentsAreAnonymous:
		if implements && len(declared) > 0 {
			t.Errorf("connector %q states its attachments are fetched anonymously but declares credential hosts %v.\n"+
				"A pre-signed URL already carries its authorisation; also sending the credential widens where the secret travels for no benefit.\n"+
				"Either drop CredentialHosts() or state AttachmentsAreAuthenticated.", name, declared)
		}

	case AttachmentsAreNotSupported:
		if implements && len(declared) > 0 {
			t.Errorf("connector %q archives no attachments but declares credential hosts %v; the declaration can only widen exposure", name, declared)
		}

	default:
		t.Fatalf("unknown AttachmentFetching value %d", subject.AttachmentIntent)
	}

	// Whatever was declared, Core's matching rules are exact. A connector
	// author who expects a wildcard learns it here rather than in production.
	for _, host := range declared {
		if strings.HasPrefix(host, "*") || strings.Contains(host, "*") {
			t.Errorf("declared host %q looks like a wildcard; Alih matches hosts exactly and case-insensitively, with no wildcards and no subdomain inheritance", host)
		}
	}
}

func assertUsableHost(t TB, connectorName, host string) {
	t.Helper()
	trimmed := strings.TrimSpace(host)
	switch {
	case trimmed == "":
		t.Errorf("connector %q declares an empty credential host", connectorName)
	case strings.Contains(trimmed, "/"):
		t.Errorf("connector %q declares credential host %q, which looks like a URL; declare the hostname only, without scheme or path", connectorName, host)
	case strings.Contains(trimmed, ":"):
		t.Errorf("connector %q declares credential host %q with a port; Alih compares the hostname only", connectorName, host)
	case trimmed != host:
		t.Errorf("connector %q declares credential host %q with surrounding whitespace", connectorName, host)
	}
}

// AssertFieldSemantics holds a connector's custom-field evidence to the
// vocabulary Core verification reasons about. Core never learns a provider's
// field types; it only needs to know how much a declared value can be trusted.
func AssertFieldSemantics(t TB, subject Subject) {
	t.Helper()

	if subject.FieldSemantics == nil {
		t.Skip("connector supplies no field semantics; archived field values will verify as unproven rather than be rejected")
	}
	// The interpreter must answer with one of Core's verdicts for any input,
	// including input it does not recognise and evidence a provider changed
	// underneath it. Anything else is treated as a refusal to answer.
	for _, testCase := range []struct{ name, fieldType, definition, observed string }{
		{"a field type this connector does not have", "a-type-that-does-not-exist", `{"id":"f1"}`, `"value"`},
		{"an empty field type", "", `{}`, `null`},
		{"evidence that is not JSON at all", "anything", `not json`, `also not json`},
		{"an empty definition", "anything", ``, ``},
	} {
		verdict, reason := subject.FieldSemantics.ValidateFieldValue(
			testCase.fieldType, []byte(testCase.definition), []byte(testCase.observed))
		switch verdict {
		case verify.FieldValueValid, verify.FieldValueInvalid, verify.FieldValueUnproven:
		default:
			t.Errorf("given %s, ValidateFieldValue returned verdict %q.\n"+
				"It must return %q, %q or %q. Return %q rather than guessing: Core records an unproven value as "+
				"evidence it cannot verify, which is honest, while an unknown verdict is treated as a failure to answer.",
				testCase.name, verdict,
				verify.FieldValueValid, verify.FieldValueInvalid, verify.FieldValueUnproven, verify.FieldValueUnproven)
		}
		if verdict == verify.FieldValueInvalid && strings.TrimSpace(reason) == "" {
			t.Errorf("given %s, ValidateFieldValue called the value invalid without a reason; "+
				"the reason is what a person reads in the verification result", testCase.name)
		}
	}
}

// AssertArchivePipeline drives the connector through the real M3 to M6
// implementations and holds the resulting archive to the invariants Core
// depends on but never states in an interface.
func AssertArchivePipeline(t TB, subject Subject) {
	t.Helper()

	authenticator, hasAuth := subject.Connector.(connector.Authenticator)
	extractor, hasExtract := subject.Connector.(connector.Extractor)
	switch {
	case subject.Normalizer == nil:
		t.Skip("no normalizer supplied; the archive contracts need one to turn raw evidence into the portable model")
	case !hasAuth || !hasExtract:
		t.Skip("connector does not implement both Authenticator and Extractor; the archive contracts need a live traversal")
	case subject.HTTPClient == nil:
		t.Skip("no HTTPClient supplied; the archive contracts would otherwise reach the network")
	case subject.Credential == "":
		t.Skip("no fake credential supplied; the archive contracts need one to prove it does not escape")
	}

	root := t.TempDir()
	ctx := context.Background()

	authentication, err := authenticator.Authenticate(ctx, subject.Credential)
	if err != nil {
		t.Fatalf("authenticate with the supplied fake credential: %v", err)
	}
	if len(authentication.Workspaces) == 0 {
		t.Fatal("authentication returned no workspaces; the pipeline has nothing to traverse")
	}
	workspace := authentication.Workspaces[0]

	snapshotPath := filepath.Join(root, "m3")
	session, err := snapshot.Begin(snapshotPath, subject.Connector.Name(), workspace, authentication.Identity)
	if err != nil {
		t.Fatalf("begin raw snapshot: %v", err)
	}
	extraction, err := extractor.Extract(ctx, subject.Credential, workspace, session)
	if err != nil {
		t.Fatalf("extract raw evidence: %v", err)
	}
	if _, err := session.Complete(extraction); err != nil {
		t.Fatalf("complete raw snapshot: %v", err)
	}
	evidence, err := snapshot.LoadComplete(snapshotPath)
	if err != nil {
		t.Fatalf("reload the completed snapshot: %v", err)
	}
	portable, err := subject.Normalizer.NormalizeSnapshot(evidence)
	if err != nil {
		t.Fatalf("normalize raw evidence into the portable model: %v", err)
	}

	// The portable model is checked before it is sealed. Core does reject most
	// of these, but it rejects them as a foreign-key violation or a count
	// mismatch deep inside the writer, which tells a connector author nothing.
	// Checking here means the contract is what fails, in its own words.
	assertPortableInvariants(t, portable)
	assertPortableVocabulary(t, portable)

	var credentialHosts []string
	if provider, ok := credentialHostProvider(subject); ok {
		credentialHosts = provider.CredentialHosts()
	}
	archivePath := filepath.Join(root, "archive")
	if _, err := archive.Build(ctx, evidence, portable, archivePath, archive.Options{
		HTTPClient:           subject.HTTPClient,
		Credential:           subject.Credential,
		Sleep:                func(context.Context, time.Duration) error { return nil },
		ConnectorDisplayName: displayName(subject),
		CredentialHosts:      credentialHosts,
	}); err != nil {
		t.Fatalf("build the sealed archive: %v", err)
	}

	manifest := readManifest(t, archivePath)
	assertArchiveIdentity(t, subject, manifest)
	assertVocabularyPreserved(t, subject, manifest, portable)
	verification := assertVerification(t, subject, archivePath)
	assertReportNamesTheConnector(t, subject, archivePath, manifest, verification)
	assertNoCredentialEscaped(t, subject, archivePath)
}

func assertArchiveIdentity(t TB, subject Subject, manifest archive.Manifest) {
	t.Helper()
	if manifest.SchemaVersion != archive.ArchiveSchemaVersion {
		t.Errorf("archive records manifest schema %d, want %d", manifest.SchemaVersion, archive.ArchiveSchemaVersion)
	}
	if manifest.Connector != subject.Connector.Name() {
		t.Errorf("the sealed archive records connector %q but this connector is named %q; "+
			"an archive is looked up by the name it records", manifest.Connector, subject.Connector.Name())
	}
	if want := displayName(subject); want != "" && manifest.ConnectorDisplayName != want {
		t.Errorf("the sealed archive records display name %q, want %q; "+
			"a recovery report names the provider from this field", manifest.ConnectorDisplayName, want)
	}
	if strings.TrimSpace(manifest.AlihVersion) == "" {
		t.Error("the archive records no Alih version; provenance must say which build produced it")
	}
}

// assertVocabularyPreserved is the contract B1 exists for: a connector must be
// able to describe its own objects in its own words.
func assertVocabularyPreserved(t TB, subject Subject, manifest archive.Manifest, portable model.Archive) {
	t.Helper()

	for _, neutral := range []string{"containers", "collections", "records"} {
		if _, present := manifest.Inventory[neutral]; !present {
			t.Errorf("the manifest does not record the neutral total %q; Core reconciles against these", neutral)
		}
	}

	kinds := make(map[string]struct{})
	for _, container := range portable.Containers {
		kinds["container:"+container.Kind] = struct{}{}
	}
	for _, record := range portable.Records {
		kinds["record:"+record.Kind] = struct{}{}
	}
	for kind := range kinds {
		if _, present := manifest.Inventory[kind]; !present {
			t.Errorf("the portable model uses kind %q but the manifest does not record it; "+
				"your own vocabulary must survive into the sealed archive", kind)
		}
	}
}

// assertPortableInvariants asserts the rules Core enforces during verification
// that no interface states, so a connector author meets them here rather than
// through a failed archive.
func assertPortableInvariants(t TB, portable model.Archive) {
	t.Helper()

	// Every portable identifier must be the deterministic derivation of the
	// source identity it retains. The namespace is the provider's own object
	// type for most entities -- and, for identities alone, the fixed string
	// "identity" rather than whatever the provider calls a person. That single
	// exception is invisible in any interface and is the one most likely to be
	// got wrong.
	for _, identity := range portable.Identities {
		want := model.PortableID(identity.Source.Provider, "identity", identity.Source.ID)
		if identity.ID != want {
			t.Errorf("identity %q does not carry the portable identifier Core derives for it.\n"+
				"Identities are the one entity whose portable namespace is the fixed string %q, not the provider's own "+
				"object type (%q here). Derive the ID with model.PortableID(provider, \"identity\", sourceID).",
				identity.Source.ID, "identity", identity.Source.Type)
		}
	}
	for _, container := range portable.Containers {
		if want := model.PortableID(container.Source.Provider, container.Source.Type, container.Source.ID); container.ID != want {
			t.Errorf("container %q does not carry the portable identifier Core derives from its own source type %q",
				container.Source.ID, container.Source.Type)
		}
		if container.Kind != container.Source.Type {
			t.Errorf("container %q is classified %q but retains source type %q; Core requires the two to agree, "+
				"because the kind is how your vocabulary reaches the manifest", container.Source.ID, container.Kind, container.Source.Type)
		}
	}
	for _, record := range portable.Records {
		if want := model.PortableID(record.Source.Provider, record.Source.Type, record.Source.ID); record.ID != want {
			t.Errorf("record %q does not carry the portable identifier Core derives from its own source type %q",
				record.Source.ID, record.Source.Type)
		}
	}

	// Field definitions must carry id, name and type inside their archived
	// JSON, matching the columns beside them, or verification cannot prove the
	// archived definition is the one a value was observed against.
	for _, field := range portable.FieldDefinitions {
		var definition map[string]json.RawMessage
		if err := json.Unmarshal(field.DefinitionJSON, &definition); err != nil {
			t.Errorf("field %q archives a definition that is not valid JSON: %v", field.Source.ID, err)
			continue
		}
		for _, key := range []string{"id", "name", "type"} {
			if _, present := definition[key]; !present {
				t.Errorf("the archived definition for field %q contains no %q key. "+
					"Verification compares the archived definition against the columns stored beside it and needs all three "+
					"to prove the definition was not swapped.", field.Source.ID, key)
			}
		}
		switch field.SemanticsState {
		case "SOURCE_DEFINITION_ONLY", "OBSERVED_ONLY_NO_EXECUTION":
		default:
			t.Errorf("field definition %q declares semantics state %q; it must declare %q or %q. "+
				"Use OBSERVED_ONLY_NO_EXECUTION for a computed field whose behaviour Alih does not reproduce.",
				field.Source.ID, field.SemanticsState, "SOURCE_DEFINITION_ONLY", "OBSERVED_ONLY_NO_EXECUTION")
		}
	}
	for _, value := range portable.RecordFieldValues {
		if value.SemanticsState != "OBSERVED_ONLY" {
			t.Errorf("the observed value of field %q on record %q declares semantics state %q; observed values are always %q, "+
				"because Alih records what it saw and never claims to have executed the provider's semantics",
				value.FieldID, value.RecordID, value.SemanticsState, "OBSERVED_ONLY")
		}
	}

	// Raw evidence references must point inside the archive's sealed raw tree.
	for _, record := range portable.Records {
		assertRawPath(t, "record "+record.Source.ID, record.Source.RawPath)
	}
	for _, container := range portable.Containers {
		assertRawPath(t, "container "+container.Source.ID, container.Source.RawPath)
	}
	for _, identity := range portable.Identities {
		assertRawPath(t, "identity "+identity.Source.ID, identity.Source.RawPath)
	}
}

func assertRawPath(t TB, subject, path string) {
	t.Helper()
	if path == "" {
		return
	}
	if filepath.IsAbs(path) || strings.HasPrefix(path, "..") || strings.Contains(path, "\\") {
		t.Errorf("%s retains raw evidence path %q; paths must be relative, forward-slashed and inside the archive", subject, path)
	}
	if !strings.HasPrefix(path, "raw/") {
		t.Errorf("%s retains raw evidence path %q, which does not start with %q. "+
			"Alih rewrites snapshot paths onto the archive's sealed raw tree; return the path your sink recorded and let Core rewrite it",
			subject, path, "raw/")
	}
}

func assertVerification(t TB, subject Subject, archivePath string) verify.Report {
	t.Helper()
	verification, err := verify.Archive(archivePath, verify.Options{FieldSemantics: subject.FieldSemantics})
	if err != nil {
		t.Fatalf("verify the archive this connector produced: %v", err)
	}
	if verification.Failed() {
		t.Fatalf("the archive this connector produced does not verify: %s\n%#v", verification.Result, verification.Checks)
	}
	return verification
}

func assertReportNamesTheConnector(t TB, subject Subject, archivePath string, manifest archive.Manifest, verification verify.Report) {
	t.Helper()
	document := report.Build(report.Inputs{
		ArchivePath: archivePath, Manifest: manifest, ManifestAvailable: true,
		Verification: verification, GeneratedAt: time.Unix(0, 0).UTC(), AlihVersion: "conformance",
	})
	var rendered strings.Builder
	if err := report.RenderText(&rendered, document); err != nil {
		t.Fatalf("render the recovery report: %v", err)
	}
	text := rendered.String()
	if strings.Contains(text, "{connector}") {
		t.Error("the recovery report contains an unresolved {connector} placeholder")
	}
	want := displayName(subject)
	if want == "" {
		want = subject.Connector.Name()
	}
	if !strings.Contains(text, want) {
		t.Errorf("the recovery report never names %q; a person reading it must know which provider the data came from", want)
	}

	output := filepath.Join(t.TempDir(), "view")
	if _, err := organize.Build(context.Background(), archivePath, output, organize.Options{
		Verifier: passingVerifier{verification}, AlihVersion: "conformance",
	}); err != nil {
		t.Fatalf("organize the archive this connector produced: %v", err)
	}
}

// assertNoCredentialEscaped searches everything the run published for the fake
// secret it was given.
func assertNoCredentialEscaped(t TB, subject Subject, archivePath string) {
	t.Helper()
	if subject.Credential == "" {
		return
	}
	err := filepath.WalkDir(archivePath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), subject.Credential) {
			relative, _ := filepath.Rel(archivePath, path)
			t.Errorf("the credential appears in the sealed archive at %s; "+
				"a credential must never be written into evidence, a manifest, or a database", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type passingVerifier struct{ report verify.Report }

func (v passingVerifier) Verify(string) (verify.Report, error) { return v.report, nil }

func credentialHostProvider(subject Subject) (connector.CredentialHostProvider, bool) {
	if provider, ok := subject.Connector.(connector.CredentialHostProvider); ok {
		return provider, true
	}
	if subject.Normalizer != nil {
		if provider, ok := subject.Normalizer.(connector.CredentialHostProvider); ok {
			return provider, true
		}
	}
	return nil, false
}

func displayName(subject Subject) string {
	if namer, ok := subject.Connector.(interface{ DisplayName() string }); ok {
		if name := strings.TrimSpace(namer.DisplayName()); name != "" {
			return name
		}
	}
	if subject.Normalizer != nil {
		return strings.TrimSpace(subject.Normalizer.DisplayName())
	}
	return ""
}

func sameCapabilities(first, second []connector.Capability) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func readManifest(t TB, archivePath string) archive.Manifest {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(archivePath, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest archive.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// assertPortableVocabulary proves a connector describes its own objects in its
// own words. Core counts neutrally and stores the connector's kinds beside the
// totals, so a connector never has to borrow another provider's nouns -- and a
// connector that does is almost always copying an example rather than modelling
// its own source.
func assertPortableVocabulary(t TB, portable model.Archive) {
	t.Helper()
	foreign := map[string]struct{}{
		"space": {}, "spaces": {}, "folder": {}, "folders": {}, "list": {}, "lists": {},
		"task": {}, "tasks": {}, "subtask": {}, "subtasks": {},
	}
	for _, container := range portable.Containers {
		if _, borrowed := foreign[strings.ToLower(container.Kind)]; borrowed {
			t.Errorf("container %q is classified %q. That is another provider's word, and Alih never requires it: "+
				"Core counts neutrally as containers, collections, records and nested_records, and records your own kind "+
				"beside those totals. Use the word your source uses.", container.Source.ID, container.Kind)
		}
	}
	for _, record := range portable.Records {
		if _, borrowed := foreign[strings.ToLower(record.Kind)]; borrowed {
			t.Errorf("record %q is classified %q. That is another provider's word, and Alih never requires it: "+
				"use the word your source uses.", record.Source.ID, record.Kind)
		}
	}
}

// AssertOperationalErrors holds a connector's failures to the error contract.
//
// Core never reads a provider's error text. It asks the error what it means,
// and acts on a stable reason code. A connector whose failures carry no
// assessment still works, but every failure it produces becomes an
// indistinguishable UNKNOWN_FAILURE: a rejected credential, a rate limit and an
// outage all look the same to status, events and notifications.
func AssertOperationalErrors(t TB, subject Subject) {
	t.Helper()

	if len(subject.SampleErrors) == 0 {
		t.Skip("connector supplies no sample errors; its failures will be reported as UNKNOWN_FAILURE, which is safe but tells a user nothing about what to do")
	}
	observed := time.Unix(0, 0).UTC()
	seen := make(map[connector.HealthReason]struct{}, len(subject.SampleErrors))

	for _, sample := range subject.SampleErrors {
		if sample == nil {
			t.Error("a nil error was supplied as a sample; supply the failures this connector actually returns")
			continue
		}
		assessment, ok := connector.AssessmentFromError(sample, observed)
		if !ok {
			t.Errorf("the error %q carries no operational assessment.\n"+
				"Core classifies failures by asking the error, never by matching its text. Implement "+
				"OperationalAssessment(time.Time) on this error type so a rejected credential is not reported "+
				"the same way as an unreachable provider.", sample)
			continue
		}
		if err := connector.ValidateOperationalAssessment(assessment); err != nil {
			t.Errorf("the assessment carried by %q is not valid: %v", sample, err)
			continue
		}
		if assessment.Health.Connector != subject.Connector.Name() {
			t.Errorf("the assessment carried by %q names connector %q, but this connector is named %q",
				sample, assessment.Health.Connector, subject.Connector.Name())
		}
		// A failure must land somewhere specific. Health and authentication are
		// deliberately separate: a rejected credential leaves the provider
		// HEALTHY and says so through the authentication observation instead,
		// because blaming the provider for a bad token would send an operator
		// to the wrong place. What is not acceptable is a failure that
		// classifies as neither.
		credentialProblem := assessment.Authentication.State == connector.AuthenticationRejected ||
			assessment.Authentication.State == connector.AuthenticationRequired
		providerProblem := assessment.Health.State != connector.HealthHealthy
		if !credentialProblem && !providerProblem {
			t.Errorf("the error %q reports the provider as %q and authentication as %q, so nothing is wrong anywhere.\n"+
				"Every failure must classify as either a credential problem or a provider problem, or an operator "+
				"cannot be told what to do about it.",
				sample, assessment.Health.State, assessment.Authentication.State)
		}
		if providerProblem && assessment.Health.Reason == connector.HealthReasonNone {
			t.Errorf("the error %q reports the provider as %q but carries no reason code; Core has nothing stable to act on",
				sample, assessment.Health.State)
		}
		seen[assessment.Health.Reason] = struct{}{}
	}
	if len(seen) < 2 && len(subject.SampleErrors) > 1 {
		t.Errorf("every sample error maps to the same reason %v.\n"+
			"If this connector genuinely cannot tell its failures apart, supply one sample. A provider that "+
			"distinguishes a rejected credential from a rate limit or an outage should classify them differently, "+
			"because that is what status and notifications report.", seen)
	}
}
