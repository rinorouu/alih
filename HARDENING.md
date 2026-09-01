# Alih hardening traceability matrix

This document maps each operational failure scenario Alih must survive to the
automated tests that prove it. It exists so that "we handle that" is always a
claim somebody can check, and so that a scenario cannot quietly lose its
coverage while the prose stays reassuring.

Every test named here is verified to exist by
`internal/hardening.TestEveryScenarioNamesTestsThatExist`. Renaming a test
without updating this file fails the build. The matrix is therefore a
executable claim, not documentation that drifts.

## What the matrix is asserting

Across every scenario, three things must never happen:

1. **Silent loss** — data Alih was expected to archive is missing and nothing
   says so.
2. **False success** — a run that did not achieve what it claims exits zero,
   emits `operation.completed`, prints a completion message, or leaves a
   status that reads healthy.
3. **Hidden ambiguity** — Alih does not know what happened and presents that as
   if it did.

A test earns a place below only if it would fail were one of those introduced.

---

## 1. Expired, revoked, or rejected credentials

A rejected credential must be attributed to the credential, never to the
provider, and must never be echoed back.

| Claim | Test |
| --- | --- |
| A rejected token stops the run and is classified, not retried | `TestAuthenticateClassifiesRejectedTokenAndDoesNotContinue` |
| Authentication state is separate from provider health | `TestErrorOperationalAssessment` |
| A rejected credential is recorded for every scope of that connector | `TestStatusRefreshRecordsARejectedCredentialForEveryScopeOfThatConnector` |
| A rejected credential is its own event, and does not accuse the provider | `TestARejectedCredentialIsItsOwnEvent` |
| No failure path echoes the token | `TestCredentialFailuresNeverEchoTheToken`, `TestBackupRedactsCredentialFromErrors` |
| A failed auth saves nothing and claims no partial success | `TestAuthFailureDoesNotSaveCredentialOrPrintPartialSuccess` |
| A missing credential leaves provider health `UNKNOWN`, not failed | `TestAuthReportsMissingCredential` |

## 2. Rate limiting

Bounded retry, then an explicit failure. Never an unbounded wait and never a
partial result presented as complete.

| Claim | Test |
| --- | --- |
| A 429 is retried and the failed attempt stays in the ledger | `TestGetRetriesRateLimitWithRecordedFailureThenRecordsRawSuccess` |
| Retries are bounded and then fail closed | `TestGetExhaustsRetriesAndFailsClosed` |
| An unusable `Retry-After` is ignored rather than trusted | `TestRetryDelayIgnoresUnusableRateLimitHeaders` |
| Rate limiting maps to a stable reason, not parsed text | `TestAuthenticateClassifiesRateLimit`, `TestErrorOperationalAssessment` |
| A retryable webhook failure is retried under one idempotency key | `TestWebhookRetriesOnlyRetryableFailuresWithOneIdempotencyKey` |

## 3. Endpoint removal and changed API behaviour

An endpoint that disappears is a capability problem, not a mysterious error.

| Claim | Test |
| --- | --- |
| 404 maps to `CAPABILITY_REMOVED` and names the affected capability | `TestErrorOperationalAssessment` |
| A removed required capability cannot aggregate to healthy | `TestAggregateCapabilityHealth`, `FuzzAggregateCapabilityHealthNeverInventsHealth` |
| A missing required hierarchy array fails closed | `TestScanRejectsMissingRequiredHierarchyArray` |
| A non-retryable status is not retried | `TestNonRetryableStatusIsNotRetried` |
| Unknown provider fields do not break extraction | `TestUnknownProviderFieldsDoNotBreakExtraction` |

## 4. Malformed and oversized responses

| Claim | Test |
| --- | --- |
| An oversized response is rejected rather than buffered | `TestOversizedResponseIsRejected` |
| A malformed workspace response is refused | `TestAuthenticateRejectsMalformedAndDuplicateWorkspaceResponses` |
| A raw body is recorded before a parse failure, so evidence survives | `TestGetRecordsSuccessfulRawBodyBeforeParseFailure` |
| Pagination that never terminates is bounded | `TestScanFailsClosedWhenPaginationNeverTerminates` |
| A duplicate task across pages is refused, not counted twice | `TestScanRejectsDuplicateTaskAcrossPagination` |
| A subtask with no parent, or a reply-count mismatch, fails closed | `TestScanRejectsSubtaskWithMissingParent`, `TestScanRejectsThreadedReplyCountMismatch` |
| Provider text is never rendered raw into a health result | `TestScanFailureJSONUsesTypedHealthWithoutLeakingProviderText` |
| An unbounded provider message is truncated, not recorded whole | `TestAnUnboundedProviderMessageIsTruncatedNotRecordedWhole` |

## 5. Provider outage and network failure

| Claim | Test |
| --- | --- |
| A connection failure is classified as network, not authentication | `TestAuthenticateClassifiesNetworkFailure` |
| Transient failures are retried and then fail closed | `TestTransientNetworkFailureIsRetriedThenFailsClosed` |
| An unreachable provider emits `connector.unhealthy` | `TestAFailedBackupEmitsTheStageReasonAndHealthSeparately` |
| Health with no evidence is `UNKNOWN`, never healthy | `TestAggregateCapabilityHealth` |

## 6. Partial extraction

A partial traversal is never a partial archive.

| Claim | Test |
| --- | --- |
| A failed extraction returns no inventory at all | `TestExtractFailureReturnsNoPartialInventoryAndAccountsForAttempts` |
| A failed scan prints no partial inventory | `TestScanFailurePrintsNoPartialInventory` |
| An interrupted extraction returns no inventory | `TestInterruptedExtractionReturnsNoInventory` |
| Failed evidence is preserved, without the credential | `TestExtractFailureCreatesFailedEvidenceWithoutCredential`, `TestCredentialInRawResponseIsOmittedAndFailedEvidenceIsPreserved` |
| An interrupted M3 session can never load as complete | `TestInterruptedSessionIsNeverLoadableAsComplete` |
| A portable model that contradicts the snapshot is refused | `TestBuildRejectsPortableModelThatContradictsTheSnapshot` |
| Retries are accounted for in preserved partial evidence | `TestFailedTraversalAccountsForRetriesAndKeepsPartialEvidence` |

## 7. Attachment failure

Attachment content is a required capability, so losing it is not a footnote.

| Claim | Test |
| --- | --- |
| A failed download makes the archive `INCOMPLETE`, never clean | `TestBuildMarksAttachmentFailureIncompleteWithoutSilentOmission` |
| Each attachment is accounted for individually | `TestPartialAttachmentFailureIsAccountedPerAttachment` |
| A size mismatch is refused rather than archived | `TestAttachmentSizeMismatchIsRefusedRatherThanArchived` |
| An untrustworthy URL is never requested | `TestAttachmentURLsThatCannotBeTrustedAreNeverRequested` |
| A redirect drops the credential | `TestAttachmentRedirectDropsTheCredential` |
| An attachment containing the credential is refused | `TestAttachmentContainingTheCredentialIsRefused` |
| Only `attachment_content` availability is refined by retrieval | `TestBuildRefinesAttachmentContentAvailability` |
| An unresolved attachment cannot be organized into a view | `TestBuildRefusesUnresolvedAttachment` |
| A released incomplete archive still fails under a newer build | `TestAReleasedIncompleteArchiveIsStillRefused` |

## 8. Interruption

| Claim | Test |
| --- | --- |
| A real SIGTERM or SIGINT stops a real process without publishing | `TestACatchableSignalStopsTheRunWithoutPublishingIt` |
| That test fails for the right reason (negative control) | `TestAnUninterruptedHelperStillCompletes` |
| Cancellation at every pipeline stage claims no completion | `TestCancellationAtEveryStageFailsWithoutClaimingCompletion` |
| An interrupted backup cannot publish a completed bundle | `TestBackupInterruptionCannotPublishACompletedBackup` |
| An interrupted organized view publishes nothing at all | `TestBuildPublishesNothingWhenInterrupted`, `TestBuildStopsOnCancellation` |
| Organization is given a context a signal can actually cancel | `TestOrganizeReportsThePublishedView` |
| A cancelled schedule operation is honoured | `TestManagerRefusesSymlinkArtifactsAndHonorsCancellation` |
| A webhook honours timeout and cancellation | `TestWebhookTimeoutAndCancellationAreBounded` |

## 9. Disk full and unwritable storage

Modelled by injected write failures and by real permission faults.

| Claim | Test |
| --- | --- |
| The archive writer fails closed when the filesystem refuses writes | `TestBuildFailsClosedWhenTheFilesystemRefusesWrites` |
| An interrupted state write leaves the previous record intact | `TestInterruptedWriteLeavesThePreviousRecordIntact` |
| Readers never observe a partially written state record | `TestReadersNeverObserveAPartiallyWrittenRecord` |
| An event write failure is reported, never swallowed | `TestAWriteFailureIsReportedAndNeverSilentlySwallowed` |
| A raw-evidence write failure is reported | `TestRecordResponseReportsWriteFailure` |
| A staging failure publishes no organized view and leaves no residue | `TestBuildPublishesNothingWhenInterrupted` |
| An unwritable destination fails before the source is read | `TestAnUnwritableDestinationFailsBeforeTouchingTheSource` |
| A run whose state cannot be written keeps its archive but is not called healthy | `TestBackupKeepsItsResultWhenStateCannotBeRecorded`, `TestStateThatCannotBeWrittenNeverInventsAnOutcome` |
| A failure to publish the bundle is a failure, not a quiet success | `TestAFailureToPublishIsAFailureNotAQuietSuccess`, `TestAFailureToPublishIsRecordedAsAFailedAttempt` |

## 10. Corruption and tampering

| Claim | Test |
| --- | --- |
| No form of archive damage yields a verified result | `TestNoArchiveDamageEverProducesAVerifiedResult` |
| Deliberate corruption is detected per check | `TestVerifyDetectsDeliberateCorruption` |
| Damaged raw evidence is refused | `TestLoadCompleteRejectsDamagedRawEvidence`, `TestLoadCompleteRejectsCorruptRawEvidence` |
| An archive that points outside itself is refused | `TestVerifyRejectsAnArchiveThatPointsOutsideItself` |
| Symlinked raw evidence is refused | `TestBuildRefusesRawEvidenceContainingSymlinks` |
| Corrupt local state is never repaired or overwritten | `TestUnreadableStateIsNeverRepairedOrOverwritten`, `TestStatusReportsUnreadableStateWithoutRewritingIt` |
| A state directory Alih does not own is refused on read | `TestAStateDirectoryAlihDoesNotOwnIsRefusedOnRead` |
| A damaged event line never hides the rest of the history | `TestADamagedLineNeverHidesTheRestOfTheHistory` |
| Damaged history is reported without changing the status | `TestDamagedHistoryIsReportedWithoutChangingTheStatus` |
| Reconciliation never follows a symlink out of the destination | `TestReconcileNeverFollowsASymbolicLinkOutOfTheDestination` |
| A symlinked credential file is refused | `TestFileStoreRejectsSymlink` |
| A workspace name cannot escape its directory | `TestSafeWorkspaceComponentCannotEscapeItsDirectory` |

## 11. Verification failure

| Claim | Test |
| --- | --- |
| Verification failure never produces a successful backup | `TestBackupVerificationFailureNeverProducesSuccess` |
| An incomplete archive exits non-zero | `TestVerifyIncompleteArchiveExitsNonZero` |
| An incomplete export is rejected before verification | `TestBackupRejectsIncompleteExportBeforeVerification` |
| A failed archive's report never reads as safe | `TestReportFromFailedArchiveNeverReadsAsSafe` |
| The report never claims more than the verifier | `TestReportNeverClaimsMoreThanTheVerifier` |
| A required unavailable capability cannot pass as clean | `TestVerifyRejectsCleanArchiveWithUnavailableRequiredCapability` |
| An archive that cannot be verified now is never organized | `TestBuildRefusesArchivesItCannotStandBehind`, `TestBuildRefusesIncompleteRealArchive` |
| A recorded verification stops matching a changed or missing archive | `TestARecordedVerificationStopsMatchingAChangedOrMissingArchive` |

## 12. Notification failure

| Claim | Test |
| --- | --- |
| Nothing is configured means nothing is sent | `TestNotifyWithoutConfigurationIsTheSilentNoEgressDefault`, `TestNothingConfiguredMeansAlihStaysSilent` |
| Unselected events are not delivered | `TestUnselectedEventsDoNotNotify` |
| A delivery failure does not change what the run proved, and does not recurse | `TestNotificationFailureDoesNotChangeSuccessfulBackupOrRecurse` |
| A redirect is refused even with a permissive injected client | `TestInjectedClientStillCannotFollowRedirects` |
| Only a bounded response prefix is read | `TestWebhookReadsOnlyABoundedResponsePrefix` |
| A permanent rejection or missing secret is not retried | `TestWebhookDoesNotRetryPermanentRejectionOrMissingSecret` |
| A destination's URL is never exposed in what Alih renders | `TestARenderedDestinationNeverExposesWhatItsURLCarries` |
| `notification.problem` is local history and cannot be sent outward | `TestNotificationProblemIsLocalHistoryWithoutAnOutcome` |

## 13. Overlapping execution

| Claim | Test |
| --- | --- |
| Two processes cannot enter one scope; a kill releases it | `TestTwoProcessesCannotEnterOneScopeAndKillReleasesIt` |
| Overlap is refused before the pipeline starts | `TestBackupRefusesOverlapBeforeEnteringThePipeline` |
| A failed backup releases its lock | `TestFailedBackupReleasesItsOperationLock` |
| A released metadata file never acts as a stale lock | `TestReleasedMetadataFileNeverActsAsAStaleLock` |
| Overlap is an explicit skip, neither a failure nor a success | `TestScheduledOverlapIsAnExplicitSkipNotAFailureOrSuccess` |
| Lock scopes are stable, private, and independent | `TestLockScopeIsStablePrivateAndIndependent` |
| Concurrent state updates never produce an unreadable record | `TestConcurrentUpdatesNeverProduceAnUnreadableRecord` |
| Concurrent operations each record every event | `TestConcurrentOperationsEachRecordEveryEvent` |

## 14. Process kill

SIGKILL runs no cleanup, so the guarantee is narrower and stated as such.

| Claim | Test |
| --- | --- |
| A killed run publishes nothing, and what survives reads as incomplete work | `TestAnUncatchableTerminationLeavesNoPublishedBackup` |
| A killed lock owner's scope becomes acquirable again | `TestTwoProcessesCannotEnterOneScopeAndKillReleasesIt` |
| History survives a restart and a replayed append | `TestHistorySurvivesARestartAndAReplayedAppend` |
| A duplicate append collapses to one event | `TestDuplicateAppendsCollapseToOneEvent` |
| An interrupted run stays `STARTED` and reads as interrupted | `TestBackupRecordsAnInterruptedRunAsStartedNotSucceeded` |

## 15. Version upgrade

The corpus in `internal/compat/testdata` was produced by the code at tag
v0.2.4, not reconstructed by current writers.

| Claim | Test |
| --- | --- |
| The corpus really is the released shape, not a regenerated one | `TestTheCorpusIsTheReleasedShapeNotACurrentOne` |
| A released archive still verifies, and is unchanged by verifying it | `TestAReleasedArchiveStillVerifies`, `TestVerifyingAReleasedArchiveIsRepeatable` |
| A released archive keeps its own version and capability shape | `TestAReleasedArchiveKeepsItsOwnProvenance`, `TestAnExistingArchiveIsNeverRewrittenToCarryANewVersion` |
| Released raw evidence still loads | `TestAReleasedRawSnapshotStillLoads` |
| A released archive still reports and still organizes | `TestAReleasedArchiveStillProducesARecoveryReport`, `TestAReleasedArchiveCanBeOrganized` |
| Pre-contract capability evidence is never reinterpreted as supported | `TestLegacyCapabilityEvidenceRemainsReadableWithoutInference`, `TestPreContractCapabilitySnapshotRemainsReadable`, `TestPreContractManifestV2RemainsVerifiable` |
| Every recorded state and event version still loads | `TestEveryRecordedStateVersionStillLoads`, `TestEveryRecordedEventVersionStillLoads` |
| Reading old state does not rewrite it on disk | `TestReadingOldStateNeverRewritesItInPlace` |
| A future schema is refused, not guessed | `TestAFutureStateSchemaIsRefusedNotGuessed`, `TestAFutureEventSchemaIsSkippedNotMisread`, `TestUnmarshalRefusesUnknownFieldsAndNewerSchemas` |
| A migration invents no evidence the old document never held | `TestStateVersionOneMigratesWithoutChangingItsScopeKey`, `TestVersionOneEventMigratesWithoutInventingScheduleEvidence` |
| One injected release identity reaches every artifact a run writes | `TestOneReleaseVersionReachesEveryArtifactARunWrites` |

---

## Cross-cutting properties

These are not scenarios but invariants every scenario is measured against.

| Property | Test |
| --- | --- |
| No failing command prints a success claim | `TestFailingCommandsNeverPrintASuccessClaim` |
| The exit-code contract holds across commands | `TestExitCodeMatrix` |
| No event type can carry a credential or a provider body | `TestNoEventTypeCanCarryACredentialOrProviderBody` |
| Recorded state never contains the credential | `TestRecordedStateNeverContainsTheCredential` |
| Status JSON is pure, stable, and free of the credential | `TestStatusJSONIsPureStableAndFreeOfTheCredential` |
| An archive is refused if it contains the credential | `TestBuildRefusesCredentialInRawSnapshot` |
| Generated scheduler artifacts contain no credential | `TestNativePlansAreDeterministicBoundedAndContainNoCredential` |
| The credential reaches only hosts the connector declared | `TestTheCredentialReachesOnlyDeclaredHosts`, `TestNoProviderHostnameIsCompiledIntoTheArchiveWriter` |
| A credential is stored and read per connector, never globally | `TestTwoConnectorsCoexistWithoutOverwritingEachOther`, `TestCredentialAccessIsScopedToTheWiredConnector` |
| A credential file this build cannot read is refused, not overwritten | `TestAnUnsupportedCredentialSchemaIsRefusedNotGuessed` |
| A connector identifier is validated before it selects a credential | `TestConnectorNameIsValidatedBeforeItSelectsACredential` |
| A provider-signed URL never escapes raw evidence | `TestTheProvableSurfacesNeverCarryASignedURL`, `TestReleasedArchivesCarryNoAlihCredential` |
| Ordering uses position, not the wall clock | `TestOrderingUsesPositionNotTheWallClock` |
| Status survives a clock that moved backwards | `TestStatusSurvivesAClockThatMovedBackwards` |
| Recorded history never decides the status | `TestRecordedHistoryNeverDecidesTheStatus` |
| Archive output is deterministic regardless of input ordering | `TestArchiveIsDeterministicRegardlessOfAttachmentOrdering`, `TestBuildIsDeterministic`, `TestBuildIsIndependentOfPhysicalRowOrder` |
| A target that already exists is never overwritten | `TestBuildRefusesTargetThatAlreadyExists`, `TestBackupDoesNotOverwriteAnExistingBundle` |

## Bounded resources

| Bound | Test |
| --- | --- |
| Response size | `TestOversizedResponseIsRejected` |
| Pagination | `TestScanFailsClosedWhenPaginationNeverTerminates`, `TestScanHandlesLargePaginatedWorkspace` |
| Retry count | `TestGetExhaustsRetriesAndFailsClosed` |
| Event log size, dropped by whole files | `TestTheLogIsBoundedAndDropsWholeFilesNotLines` |
| Webhook response prefix and delivery bounds | `TestWebhookReadsOnlyABoundedResponsePrefix`, `TestDeliveryBoundsAreClampedNotTrusted` |
| Operator message length | `TestAnUnboundedProviderMessageIsTruncatedNotRecordedWhole`, `TestResolveNeverReturnsUnsafeOrUnboundedText` |
| Normalization stays linear in workspace size | `TestNormalizeLargeWorkspaceStaysLinear` |
| Organized view streams a large dataset | `TestLargeArchiveStreamsWithBoundedWork` |
| Organized view does not accumulate file descriptors | `TestGenerationDoesNotAccumulateOpenFiles` |
| Generated path components are length-bounded | `TestComponentBoundsLength` |

## Property and fuzz coverage

Where the space of inputs is larger than a table can enumerate.

| Target | Test |
| --- | --- |
| Generated path components are always portable and inescapable | `FuzzComponentIsAlwaysOnePortableName` |
| Attachment filenames are always one safe component | `FuzzAttachmentComponentIsAlwaysPortable` |
| Relative write paths never escape the staging root | `FuzzSafeRelativeNeverAcceptsAnEscape` |
| An accepted state record always validates and round-trips | `FuzzUnmarshalNeverPanicsAndNeverAcceptsAnInvalidRecord` |
| An accepted event is always safe and always one line | `FuzzUnmarshalNeverPanicsAndNeverAcceptsAnUnsafeEvent` |
| Health aggregation never invents health | `FuzzAggregateCapabilityHealthNeverInventsHealth` |

## Known limits, deliberately not closed here

These are recorded rather than tested, because the honest answer is a boundary
rather than a guarantee.

- **A version 1 credential file names its provider inline.** Reading it back
  requires knowing that name, so `internal/credentials` keeps one provider
  constant on purpose. Core's other credential-path packages carry none, which
  `TestCoreNamesNoProviderInItsCredentialPath` enforces.
- **Authentication failure before a destination is resolved records nothing.**
  No scope exists to key it to. The operator still sees the full assessment on
  stderr. Covered as a deliberate limit by
  `TestBackupFailureBeforeADestinationIsKnownRecordsNothing`.
- **Cross-process state writes narrow rather than close the compare-and-set
  window.** The residual cost is one lost attempt record; the file is never
  corrupted and no success is invented. The operation lock, not the state file,
  is what prevents overlapping runs.
- **Windows path length is bounded by design, not guaranteed.** The deepest
  organized-view path for a minimal workspace is 177 characters, but a deep
  workspace under a deep destination can still exceed 260 on a Windows
  installation without long-path support.
- **Raw evidence contains provider-signed attachment URLs.** This is required
  by the "exact response bytes" guarantee. The boundary that is tested is that
  such a URL never escapes `raw/` into any provable or operational surface.
- **SIGKILL runs no cleanup.** A killed run may leave a `.partial-` working
  directory. What is guaranteed, and tested, is that nothing left behind reads
  as a completed backup.
