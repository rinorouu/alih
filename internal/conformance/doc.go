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

// Package conformance is the connector conformance suite: the executable form
// of what Alih Core requires from a connector.
//
// A connector's own test calls Run with a Subject describing the adapter, and
// gets one subtest per contract. Contracts the subject cannot support are
// skipped with the reason, so a passing run never quietly means nothing ran:
//
//	func TestMyConnectorConforms(t *testing.T) {
//		conformance.Run(t, conformance.Subject{
//			Connector:        mysource.New(),
//			Normalizer:       mysource.Normalizer{},
//			AttachmentIntent: conformance.AttachmentsAreAnonymous,
//		})
//	}
//
// The suite is ordinary Go testing. There is no registration, no reflection
// over connector types, no generated code and no runner of its own, because a
// connector author should be able to read what a failure means without first
// learning a framework.
//
// # Why the contracts live here rather than in prose
//
// Several rules Core depends on are stated in no interface: the portable
// identity namespace is the fixed string "identity" rather than the provider's
// own word for a person; an archived field definition must carry id, name and
// type; observed values are always OBSERVED_ONLY; raw evidence paths are
// rewritten onto the archive's sealed raw tree. Core does reject archives that
// break these, but it rejects them as a foreign-key violation or a count
// mismatch deep inside the writer. The suite checks them first so the failure
// is the contract, in its own words.
//
// # The one fact the suite cannot infer
//
// connector.CredentialHostProvider is optional and fail-closed: a connector
// that declares no hosts gets no credential attached to any attachment
// request. That is right for a pre-signed link and wrong for a provider that
// expects authentication, and Core cannot tell the two apart. The second case
// otherwise surfaces as an unexplained upstream 401. Subject.AttachmentIntent
// is where a connector states which it is, in the test fixture rather than in
// the production contract, so the shipped behaviour stays fail-closed and
// optional while the suite can still hold a connector to its own intent.
//
// # The reference subjects
//
// The fake adapter in this package names no real SaaS and deliberately uses a
// vocabulary that is not ClickUp's — sections, boards, items — so that Core
// reintroducing another provider's assumptions fails here. ClickUp runs the
// same suite from its own package; it passes because it satisfies the
// contracts, not because they were written around it.
//
// The suite's own failures are tested too. A conformance check that cannot be
// shown to fail is not evidence, so mutation_test.go breaks one contract at a
// time and asserts the suite says which one.
package conformance
