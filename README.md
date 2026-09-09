# iro

`iro` は、GitHub Issue を WHAT / WHY の正本、Pull Request を implementation / review の場として扱う Issue-driven Repository Orchestrator です。Issue ごとの Git worktree で disposable な Codex worker を実行し、Git と GitHub に durable state を残します。OpenAI Symphony の設計思想に着想を得ていますが、fork、公式配布物、または OpenAI の公式実装ではありません。

```text
Executable Issue
  → iro run
  → normal open PR
  → optional iro review / iro revise
  → Human judgment
  → iro land
  → merge / GitHub native Issue close

必要なら後で: iro cleanup
```

## Install

Go toolchain と Git を事前に用意してください。`iro run`、`iro review`、`iro revise` には `gh` CLI、Codex CLI、および各認証も必要です。`iro land` は `gh` CLI と GitHub 認証を必要とし、Codex は要求しません。現在 support する configured remote host は `github.com` のみです。`iro` は不足する環境や認証を自動構築しません。

```bash
go install ./cmd/iro
go env GOBIN
go env GOPATH
command -v iro
iro version
```

repository の source root で install します。install 先は設定済みの `GOBIN`、未設定なら通常 `$(go env GOPATH)/bin` です。その directory が `PATH` に含まれている必要があります。`command -v iro` で選ばれる binary と `iro version` の build 情報を確認してください。PATH の設定は Human が利用環境に合わせて行います。

## Commands

`iro version` は project 外でも実行できます。その他は既存の Git repository の root またはその配下で実行します。

```bash
iro version
iro init
iro doctor
iro run <issue-number>
iro review <pr-number>
iro revise <pr-number>
iro land <pr-number>
iro status
iro cleanup <issue-number>
```

`iro init` は repository root に `iro.toml` と `WORKFLOW.md` を新規生成する local scaffold operation です。既存 file を上書きせず、commit や push も行いません。生成した file を Git へ記録するかどうかは Human が判断します。

`iro doctor` は環境・認証・設定を read-only で診断し、iro / git / gh / codex の executable path と version、project / repository の識別情報も表示します。`iro status` は ownership mapping に対応する local Issue workspace の機械状態だけを read-only で表示し、Issue や PR の進捗を推測しません。

## Remote delivery

### Run

`iro run <issue-number>` は configured repository の default branch を canonical delivery base とします。開始時の checkout は clean かつその default branch の named checkout でなければならず、non-default branch や detached HEAD からは開始しません。その検証済み local HEAD から `iro/issue-N` branch と canonical Issue worktree を作り、fresh Author worker を実行します。

worker 成功後は iro が変更を commit / push し、`iro/issue-N` を head、default branch を base、Issue `#N` を GitHub native closing relation とする通常の open PR を作成します。iro 自身は Draft PR を作りません。Author report は先に local log へ保存し、成功時は `iro land` の案内と同じ delivery PR comment に集約します。Issue へ成功 report は投稿しません。PR comment 投稿失敗は warning に留め、Run の成功を覆しません。Author failure または stage / commit / push / PR create 等の delivery failure 時は、origin Issue へ Author report と診断の投稿を試みます。その投稿失敗は元の operation failure を隠さず、追加 diagnostic として表示します。自動 retry / rollback / repair は行いません。delivery comment は Human 向け UX にすぎず、remote state、ownership、creator provenance、後続 operation の eligibility の正本ではありません。

### Review and Revise

`iro review <pr-number>` は optional / advisory です。configured repository の default branch を base とし、exactly 1 件の同 repository内 origin Issue への GitHub native closing relation を持つ open PR を、fresh で独立した Reviewer が disposable workspace で評価します。target の local branch、Issue worktree、ownership mapping は不要で、Draft や Human / fork 由来の PR も relation を満たせば review できます。

Review report では、開始時に観測した base branch / base OID と、disposable workspace の HEAD と一致を検証した Reviewed HEAD OID を識別できる trusted provenance を Reviewer へ渡します。resolved model identity を runtime interface から確実に取得できない場合は推測せず、取得不能であることを明示します。これは Human が review 対象 snapshot を後から識別するための情報であり、review freshness gate や Land authorization ではありません。

Reviewer の final response は opaque text です。iro は provenance、`PASS` / `FINDING`、format を parse / normalize / 再構成せず、response 全体をそのまま PR comment へ forward します。`FINDING` でも command 自体は成功し得ます。

Review は prompt-isolation の security boundary ではありません。Reviewer は PR HEAD 上で動くため、PR が `AGENTS.md` などの agent instruction file を変更する場合は、その影響も考慮して Human または独立 session で追加確認してください。

`iro revise <pr-number>` は fresh Author で既存の delivery PR を更新します。PR は configured repository の `iro/issue-N` を head、default branch を base とし、GitHub native closing Issues が exactly `{N}`、その Issue / canonical branch の active delivery PR が target だけでなければなりません。PR creator identity や iro-created marker は要求せず、Human が canonical relation で作成した PR も対象です。

canonical local mapping / branch / worktree がすべて欠落していれば、validated remote PR HEAD から materialize できます。一貫して clean で local HEAD が remote PR HEAD と一致する state は再利用しますが、partial、dirty、divergent な state は自動修復しません。成功後は iro が新しい commit を同じ branch へ通常 push し、同じ PR を更新します。

### Land

`iro land <pr-number>` の明示的な invocation 自体が、その PR に対する Human の merge authorization です。target selection と品質判断は Human が所有します。AI Review の実行や内容、PR creator identity、delivery hint、local Issue branch / worktree / ownership mapping は Land precondition ではありません。

Land は configured repository の default branch を base、同 repository の `iro/issue-N` を head、GitHub native closing Issues を exactly `{N}` とする一意な active delivery relation、および repository merge policy を検証します。Draft PR は対象外で、iro は自動的に Ready for review へ変更しません。

validation で取得した PR HEAD OID を実際の normal merge operation に bind するため、検証後の HEAD drift は merge failure になります。`mergeStateStatus == BEHIND` であることだけでは拒否せず、validated HEAD を指定して merge を試み、up-to-date requirement などの最終判断を GitHub の repository policy に委ねます。policy rejection や HEAD drift 時に admin bypass、branch auto-update、自動 retry、別 merge method への fallback は行いません。

Land の authentication、GraphQL、merge API は configured host の `github.com` へ明示的に bind され、`GH_HOST` / `GH_REPO` などで別 host や repository へ reroute されません。Land は remote delivery の完了だけを担い、local cleanup や remote branch の明示的削除は行いません。merge 後の Issue close は GitHub native closing relation に委ねます。

### Cleanup

`iro cleanup <issue-number>` は Land とは独立した local resource operation です。Human が明示した Issue について、ownership mapping で所有を検証できる clean な canonical worktree と local branch だけを安全に削除し、最後に mapping を削除します。GitHub Issue / PR や remote branch は確認・変更しません。

## Responsibility boundary

Human は repository を直接操作する authority、仕様判断、command の target selection、最終 merge judgment を所有します。Human は canonical branch への commit / push や delivery PR 作成を直接行えます。

Author / Reviewer worker は disposable です。working tree file の変更や検証は行えますが、Git metadata / history / remote state と tracker lifecycle を変更しません。commit、push、PR / comment 作成、merge、verified local cleanup は、Human が明示した operation の範囲で iro が担います。daemon、scheduler、自動 merge、自動 retry loop、Codex session resume は提供しません。

runtime contract の詳細は [`docs/behavior.md`](docs/behavior.md)、現在構成の non-normative diagrams は [`docs/architecture.md`](docs/architecture.md) を参照してください。

古い binary の確認や Run failure 後の保存・破棄・再実行は、[operator cookbook](docs/cookbook.md) を参照してください。
