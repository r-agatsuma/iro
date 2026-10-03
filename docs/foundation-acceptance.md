# #83 foundation 統合 acceptance

この文書は #95 の検証範囲を追跡する non-normative な索引である。runtime requirement の正本は [behavior.md](behavior.md)、architecture の図は [architecture.md](architecture.md) とする。

#88〜#94 の実装は統合対象に存在する。GitHub + Codex の foundation を historical #72〜#81 の replacement planning の出発点とし、PR #82 の修復・採用、selector / backend / agent の拡張は含めない。

| Slice | 統合対象の実装 |
|---|---|
| F1 / #88 | [raw-body origin resolver / writer](../internal/iro/github_origin.go) |
| F2 / #89 | [delivery allocation](../internal/iro/delivery_identity.go)、[local inventory](../internal/iro/local_inventory.go)、[workspace naming / recognition](../internal/iro/workspace_namespace.go) |
| F3 / #90 | [Status](../internal/iro/status.go)、[Cleanup](../internal/iro/cleanup.go) |
| F4 / #91 | [Run producer](../internal/iro/run_producer.go)、[managed delivery](../internal/iro/delivery.go)、[unmanaged Run](../internal/iro/unmanaged.go) |
| F5 / #92 | [Review](../internal/iro/review.go) |
| F6 / #93 | [Revise](../internal/iro/revise.go) |
| F7 / #94 | [Land](../internal/iro/land.go) |

## 統合経路と回帰検証

追加した [foundation integration tests](../internal/iro/foundation_integration_test.go) は、Run が生成した body / refs / registered paths を後続 consumer と local lifecycle に渡す。以下の既存 slice / cross-mode tests も同じ acceptance suite に含める。

| Required path | 検証と確認する境界 |
|---|---|
| 1. Run → relation consumers | `TestRunProducerFixturesRoundTripThroughManagedReview` / `TestRunProducerFixturesRoundTripThroughManagedRevise`：managed `Closes #N` と unmanaged `Refs #N` の実際の writer payload を F1 resolver に通す。`TestManagedReviewIgnoresDeliveryTopology` / `TestReviseSupportsSelectedRefsWithoutDeliveryTopology`：producer provenance、native closing、default base、v1 JSON に依存しない |
| 1. managed / unmanaged authority | `TestUnmanagedG4OriginAndManagedConfiguredRemoteRemainIndependent`、`TestReviewModeSpecificIdentity`、`TestReviseModeSpecificIdentity`、`TestLandModeSpecificIdentity`：configured remote と origin を独立に bind。`TestUnmanagedReviseG1PreservesManagedOwnershipAndRejectsStaleLocalState` / `TestUnmanagedReviseG3ExplicitIssueDoesNotReplaceManagedOrigin` / `TestUnmanagedReviseG5BuiltInPolicyThenManagedStartingPolicy`：local state、explicit Issue、worker policy の非採用。`TestFoundationUnmanagedRunResidueDoesNotSupplyManagedAuthority`：actual unmanaged writer と retained detached residue を渡し、managed project authority の未補完、独立した Review policy / H1 Revise policy、新規 attached materialization と residue の非採用を確認 |
| 2. Multiple delivery identity | `TestRunProducesIndependentDeliveries` と `TestFoundationDeliveriesBodyRebindAndPhysicalCleanup`：同じ Issue の複数 ref / workspace / report を分離し、それぞれを consumer へ渡す。`TestManagedReviseAllowsSharedHead` / `TestLandIgnoresOtherPRsSharingHeadOrOrigin`：one-active / canonical / global shared-head gate を使わない |
| 3. Body rebind と physical Cleanup | `TestFoundationDeliveriesBodyRebindAndPhysicalCleanup`：次の Review / Revise は body の Issue 124 を解決し、`cleanup 124` は Issue 123 の delivery を削除しない。`cleanup 123` は元の両 delivery を削除。`TestManagedReviewBindsStartingBodyAndVerifiedHead` / `TestReviseBindsBodyOnceAndAllowsBodyOrBaseChanges`：実行中の binding は固定 |
| 4. Revise fault matrix | `TestRevisePushTargetFaultsKeepSingleChildWithoutPush`：advance / rewind / delete / closed / merged / head repository・ref / unreadable PR・ref / destination・identity change で H1-parent C1 を保持し push しない。`TestReviseCommitIntegrityAndUnknownPush`：sole parent / staged tree integrity と unknown push。`TestReviseBindsBodyOnceAndAllowsBodyOrBaseChanges`：body / base-only change を許可 |
| 4. Accepted final-read race | `TestFoundationReviseFinalReadNormalPushRace`：commit 後の target read は一度だけ。通常 push のまま rewind 後の受理、advance 後の拒否、push unknown を扱い、lease / CAS / lock / retry を追加しない。保持された C1 は Status に見え、明示 Cleanup で local purge できる |
| 5. Land | `TestLandDoesNotRequireDeliveryTopologyOrOrigin` / `TestLandForkDoesNotRequireHeadRepositoryWritePermission` / `TestManagedLandForkAcceptanceDoesNotWidenUnmanagedAuthority`：origin-independent な managed same-repository / fork head。`TestLandRejectsInvalidTargetBeforeMerge`：configured repository permission / policy。`TestLandBindsValidatedHeadAndDoesNotRetryChangedHead`：exact HEAD-bound 一度の merge。`TestLandDraftRemediationKeepsOperationMode` / `TestLandMergeFailureDoesNotRetryRepairOrFallback`：mode guidance と unknown outcome。Issue closure を要求しない |
| 6. Status / Cleanup の discovery / selection | `TestStatusLocalInventoryFixtures` / `TestCleanupSelectionBulkAndIssueScoped`：branch-only、attached、runtime detached、single-Issue exact matching / bulk。`TestLocalLifecycleExcludesSeparateCommonDirectory` / `TestCleanupKnownResidueOnly`：ordinary detached、別 common directory、filesystem-only unknown residue を採用・発見しない |
| 6. Destructive / best effort / non-success | `TestCleanupDirtyUntrackedIgnoredAndUnpushedStateDoesNotGate` / `TestCleanupIndependentActionsContinueAfterWorktreeFailure` / `TestCleanupRemainingAndUnknownPostStateNeverSucceeds` / `TestLocalLifecycleUnknownDiscoveryDoesNotHideFailure`：dirty / unpublished state の purge、known exact path removal、独立 target の継続、remaining / unknown / mechanism failure の non-success。namespace の symlink / missing-path 境界は `workspace_namespace_test.go` / `unmanaged_cleanup_test.go` でも検証 |
| 7. Unknown outcome の引き渡し | `TestRunRemoteFailuresDoNotRetry`：両 mode の push / PR-create / report unknown。`TestFoundationRunUnknownOutcomeHandsOffToLocalCleanup`：unknown log を保持し、Status / partial Cleanup が remote retry / repair / rollback を追加しない。`TestFoundationDeliveriesBodyRebindAndPhysicalCleanup`：Land response unknown が local delivery を変更しない。Revise unknown と local purge は final-read race test でも確認 |

`go test ./...` はこの統合状態で成功する。外部 Git / GitHub / worker の lifecycle と fault は command-runner fixture で再現し、filesystem artifact は isolated temporary directory を使う。Git transport rewrite の検証は local protocol stub を使い、remote service を変更しない。

## Baseline に残る明示的な制約

Review の body / typed validation / diff / feedback は単一 transaction ではなく、verified H1 の snapshot と取得時点の context を扱う。Revise の final read → normal push は atomic ではなく、通常 Git push の受理・拒否に委ねる。Land の exact HEAD guard は merge endpoint の `sha` に限定する。

通信断等では remote mutation が行われたか不明になり得る。automatic retry / repair / rollback は行わず、Human が現在の state を確認する。Cleanup は local-only の destructive purge であり、remote recoverability を保証しない。registration / ref / known-candidate evidence を失った filesystem-only residue の完全発見も保証しない。
