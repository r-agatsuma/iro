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

## Quick Start: Typical lifecycle

1件の Executable Issue を delivery するまでの典型的な流れです。iro が全 lifecycle を自動実行するのではなく、Human が各 operation を確認し、明示的に次の command を実行します。

1. `iro` を install し、対象の Git repository へ移動して `iro init` を実行します。

   ```bash
   cd /path/to/target-repository
   iro init
   ```

2. 生成された `iro.toml` と `WORKFLOW.md` を Human が確認します。必要な内容を調整したうえで Git に記録するかどうかも Human が判断します。`iro init` は commit や push を行いません。
3. Executable Issue を作成します。GitHub UI や ChatGPT などで Issue の作成を支援できますが、特定のサービスは必須ではありません。
4. Issue 番号を指定して実行します。

   ```bash
   iro run <issue-number>
   ```

   成功すると PR が作成されるので、Human が PR を確認します。
5. 必要な場合だけ `iro review <pr-number>` で advisory review を実行します。finding があれば、`iro revise <pr-number>` で修正してから review を繰り返せます。Review / Revise は optional です。
6. Human が PR の内容と merge を判断し、その明示的な authorization として `iro land <pr-number>` を実行します。
7. 次の `iro run` の前に必要なら local default branch を remote と同期します（例: `git pull`）。この同期は `iro` が自動実行しません。
8. 不要になった Issue worktree と local branch は、必要な場合だけ `iro cleanup <issue-number>` で個別に削除するか、`iro cleanup` で現在の repository の安全に削除できる resource をまとめて削除します。

失敗時の保存・破棄・再実行などの recovery recipe は [operator cookbook](docs/cookbook.md) を参照してください。現在の runtime contract は [`docs/behavior.md`](docs/behavior.md) に定義されています。

## Commands

`iro version` は project 外でも実行できます。その他は既存の Git repository の root またはその配下で実行します。

```bash
iro version
iro init
iro doctor
iro run <issue-number> [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]
iro review <pr-number> [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]
iro revise <pr-number> [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]
iro land <pr-number>
iro status
iro cleanup [<issue-number>]
```

`iro run`、`iro review`、`iro revise` は、番号 operand の後ろに worker configuration flag を指定できます。`--model <model>` または `-m <model>` は model だけを、`--reasoning-effort <effort>` は reasoning effort だけを operation 単位で override します。両方を指定する場合、flag の順序は問いません。各項目を指定しない場合は、その項目の選択をCodexの configuration / default に委譲します。reasoning effort は non-empty string として Codex へ渡し、iro 自身は model / effort catalog、compatibility lookup、fallback を行いません。iro は model と reasoning effort を結合した synthetic model name（例: `gpt-5.6-luna-xhigh`）を生成しません。`iro.toml` に worker configuration default を保持しません。`iro review` で指定した requested model / reasoning effort は runtime が実際に解決した configuration とは別概念であり、resolved identity を取得できない現在の Review provenance は従来どおり unknown のままです。

通常、worker は sandbox を使用します。container / VM 等で外側の isolation を用意している場合や sandbox が実行環境と干渉する場合に限り、Human が `iro run 123 --no-sandbox` のように明示して、その実行だけ Codex の sandbox を無効化できます。workspace 外や保護された Git metadata への書き込み制限も外れるため、実行環境の隔離と権限を確認して使用してください。OS の権限、container / VM、EDR、firewall 等の外側の境界や、Codex のすべての内部 policy / safety mechanism を解除するものではありません。`--model` / `-m`、`--reasoning-effort` と順序に依存せず併用でき、永続的な設定や失敗時の自動切り替えは行いません。`land` は対象外です。

`iro init` は repository root に `iro.toml` と `WORKFLOW.md` を新規生成する local scaffold operation です。既存 file を上書きせず、commit や push も行いません。生成した file を Git へ記録するかどうかは Human が判断します。

`iro doctor` は環境・認証・設定を read-only で診断し、iro / git / gh / codex の executable path と version、project / repository の識別情報も表示します。`GitHub CLI context` check では configured repository と `GH_HOST` / `GH_REPO` の不整合を検出し、configured value、observed value、修正方法を表示します。不整合時は GitHub 認証確認を実行せず、他の診断を続けて non-zero で終了します。`iro status` は ownership mapping に対応する local Issue workspace の機械状態だけを read-only で表示し、Issue や PR の進捗を推測しません。

## Remote delivery

`run` / `review` / `revise` / `land` は `tracker.remote` から解決した repository と GitHub CLI environment の整合性を、Git / worktree 変更や worker 起動、GitHub access より前に検証します。`GH_HOST` は unset / empty または `github.com`、`GH_REPO` は unset / empty または一致する `OWNER/REPO` / `github.com/OWNER/REPO` を許可します。owner/repository は configured remote と同じ規則で比較し、大文字・小文字を区別せず、末尾の `.git` を除去します。不一致や malformed な値は修正方法を stderr に表示して拒否します。Human が `unset GH_HOST` / `unset GH_REPO` または configured value への設定を行ってください。iro 自身は environment を変更しません。

実際の `gh` operation も、認証確認・API の `--hostname github.com`、host を含む repository selector、configured owner/repository の API path / GraphQL variables で接続先を明示します。

### Run

`iro run <issue-number>` は configured repository の default branch を canonical delivery base とします。開始時の checkout は clean かつその default branch の named checkout でなければならず、non-default branch や detached HEAD からは開始しません。その検証済み local HEAD から `iro/issue-N` branch と canonical Issue worktree を作り、fresh Author worker を実行します。必要な場合は `--model <model>` / `-m <model>` と `--reasoning-effort <effort>` を独立して operation 単位の override として指定できます。

worker 成功後は iro が変更を commit / push し、`iro/issue-N` を head、default branch を base、Issue `#N` を GitHub native closing relation とする通常の open PR を作成します。iro 自身は Draft PR を作りません。Author report は先に local log へ保存し、成功時は `iro land` の案内と同じ delivery PR comment に集約します。Issue へ成功 report は投稿しません。PR comment 投稿失敗は warning に留め、Run の成功を覆しません。Author failure または stage / commit / push / PR create 等の delivery failure 時は、origin Issue へ Author report と診断の投稿を試みます。その投稿失敗は元の operation failure を隠さず、追加 diagnostic として表示します。自動 retry / rollback / repair は行いません。delivery comment は Human 向け UX にすぎず、remote state、ownership、creator provenance、後続 operation の eligibility の正本ではありません。

### Review and Revise

`iro review <pr-number>` は optional / advisory です。configured repository の default branch を base とし、exactly 1 件の同 repository内 origin Issue への GitHub native closing relation を持つ open PR を、fresh で独立した Reviewer が disposable workspace で評価します。target の local branch、Issue worktree、ownership mapping は不要で、Draft や Human / fork 由来の PR も relation を満たせば review できます。`--model <model>` / `-m <model>` または `--reasoning-effort <effort>` を指定した場合だけ、それぞれ対応する requested configuration を Reviewer invocation に渡します。

Review report では、開始時に観測した base branch / base OID と、disposable workspace の HEAD と一致を検証した Reviewed HEAD OID を識別できる trusted provenance を Reviewer へ渡します。resolved model identity を runtime interface から確実に取得できない場合は推測せず、取得不能であることを明示します。これは Human が review 対象 snapshot を後から識別するための情報であり、review freshness gate や Land authorization ではありません。

Reviewer の final response は opaque text です。iro は provenance、`PASS` / `FINDING`、format を parse / normalize / 再構成せず、response 全体をそのまま PR comment へ forward します。`FINDING` でも command 自体は成功し得ます。

`iro revise <pr-number>` は fresh Author で既存の delivery PR を更新します。PR は configured repository の `iro/issue-N` を head、default branch を base とし、GitHub native closing Issues が exactly `{N}`、その Issue / canonical branch の active delivery PR が target だけでなければなりません。PR creator identity や iro-created marker は要求せず、Human が canonical relation で作成した PR も対象です。`--model <model>` / `-m <model>` と `--reasoning-effort <effort>` は、それぞれ model と reasoning effort だけを独立して Author invocationへoverrideします。

canonical local mapping / branch / worktree がすべて欠落していれば、validated remote PR HEAD から materialize できます。一貫して clean で local HEAD が remote PR HEAD と一致する state は再利用しますが、partial、dirty、divergent な state は自動修復しません。成功後は iro が新しい commit を同じ branch へ通常 push し、同じ PR を更新します。

`AGENTS.md` / `WORKFLOW.md` を変更する PR では、PR HEAD の policy が Codex behavior に影響するため、Review / Revise を prompt-isolated な security boundary とみなせません。推奨運用は `iro run` 後に Human または independent session で確認し、acceptable なら Human judgment を経て `iro land` とする流れです。不採用なら Human が PR / local workspace 等の状態を整理し、Issue specification を refine して fresh `iro run` を実行します。iro は trusted policy snapshot / provenance system や、policy rollback、PR 破棄、worktree reset、再実行の自動化を提供しません。

### Land

`iro land <pr-number>` の明示的な invocation 自体が、その PR に対する Human の merge authorization です。target selection と品質判断は Human が所有します。AI Review の実行や内容、PR creator identity、delivery hint、local Issue branch / worktree / ownership mapping は Land precondition ではありません。

Land は configured repository の default branch を base、同 repository の `iro/issue-N` を head、GitHub native closing Issues を exactly `{N}` とする一意な active delivery relation、および repository merge policy を検証します。Draft PR は対象外で、iro は自動的に Ready for review へ変更しません。

validation で取得した PR HEAD OID を実際の normal merge operation に bind するため、検証後の HEAD drift は merge failure になります。`mergeStateStatus == BEHIND` であることだけでは拒否せず、validated HEAD を指定して merge を試み、up-to-date requirement などの最終判断を GitHub の repository policy に委ねます。policy rejection や HEAD drift 時に admin bypass、branch auto-update、自動 retry、別 merge method への fallback は行いません。

Land の authentication、GraphQL、merge API は configured host の `github.com` へ明示的に bind され、`GH_HOST` / `GH_REPO` などで別 host や repository へ reroute されません。Land は remote delivery の完了だけを担い、local cleanup や remote branch の明示的削除は行いません。merge 後の Issue close は GitHub native closing relation に委ねます。merge 成功後は、次の `iro run` 前に local default branch を remote と同期するよう案内する informational hint を stdout に表示します。

```text
Sync your local default branch with the remote before the next iro run.
For example: git pull
```

iro はこの同期 command を実行せず、local checkout や branch の状態も変更・検証しません。merge failure 時にはこの success-only hint を表示しません。

### Cleanup

`iro cleanup <issue-number>` は Land とは独立した local resource operation です。Human が明示した Issue について、ownership mapping で所有を検証できる clean な canonical worktree と local branch だけを安全に削除し、最後に mapping を削除します。GitHub Issue / PR や remote branch は確認・変更しません。operand を省略した `iro cleanup` は現在の repository の mapping を Issue 番号順に処理します。DIRTY は変更せず skip し、BROKEN や削除失敗があれば残りを処理した後に non-zero を返します。

## Responsibility boundary

Human は repository を直接操作する authority、仕様判断、command の target selection、最終 merge judgment を所有します。Human は canonical branch への commit / push や delivery PR 作成を直接行えます。

iro が注入する core policy は operation / delivery lifecycle の integrity を担います。`AGENTS.md` は Codex 標準機構による project policy、`WORKFLOW.md` は repository / workload 固有の操作許可・禁止、接続方法、検証、報告要件の置き場所、Issue / PR は task data です。`iro init` の WORKFLOW scaffold を対象 workload に合わせて具体化してください。WORKFLOW は schema として parse されません。credential は環境変数や SSH agent 等の参照方法だけを記し、secret value を保存しないでください。

Run / Revise の Author は Issue scope 内の working tree file を編集できます。external workload operation は、Issue scope と WORKFLOW の明示的な許可の両方がある場合に実行できます。Issue の記載だけでは許可になりません。iro core は外部サービスの変更を一律禁止しませんが、Reviewer は WORKFLOW の許可にかかわらず read-only inspection / validation に限定され、implementation fix や external workload mutation を行いません。disposable build / test artifact は許容します。

各 worker は operation workspace の WORKFLOW を読みます。Run / Revise は canonical Issue worktree、Review は disposable PR HEAD workspace が参照元です。Review / Revise の payload に invoking repository の WORKFLOW を別途埋め込みません。正当な policy file change は repository output として扱えますが、current operation は開始時の authority boundary に従い続け、変更した policy を追加権限に使えません。

worker は disposable で、この repository の Git metadata / index / refs / history / delivery remotes と GitHub lifecycle を変更しません。WORKFLOW からもこの権限は与えられません。Author は変更を uncommitted で引き渡し、commit、push、PR / comment 作成、merge、verified local cleanup は、Human が明示した operation の範囲で iro が担います。daemon、scheduler、自動 merge、自動 retry loop、Codex session resume は提供しません。

runtime contract の詳細は [`docs/behavior.md`](docs/behavior.md)、現在構成の non-normative diagrams は [`docs/architecture.md`](docs/architecture.md) を参照してください。

古い binary の確認や Run failure 後の保存・破棄・再実行は、[operator cookbook](docs/cookbook.md) を参照してください。
