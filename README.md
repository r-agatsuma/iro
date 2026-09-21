# iro

`iro` は、GitHub Issue を AI worker への作業指示、Pull Request（PR）を実装の確認と review の単位として使う CLI です。Codex worker が Issue を実装し、必要なら独立した review と revise を行います。最後に merge するかどうかは Human が確認し、`iro land` を明示的に実行して決めます。

依頼、実際の変更、review、merge の履歴を chat session だけに閉じず、GitHub と Git に残せます。worker session は使い捨てなので、担当者や AI session が変わっても作業の経緯を追跡できます。

```text
GitHub Issue
  -> iro run
  -> Pull Request
  -> optional: iro review -> iro revise -> iro review
  -> Human confirmation
  -> iro land
```

## Quick Start

以下を上から順に進めると、1件の Issue を実装して merge するところまで試せます。

### 1. Prerequisites を確認する

この repository は Go 1.22 以上を使用します。Go が未導入なら [Download and install Go](https://go.dev/doc/install) に従い、まず version を確認してください。

```bash
go version
git --version
gh auth status --hostname github.com
codex login status
```

`iro run`、`iro review`、`iro revise` には Git、GitHub CLI (`gh`)、Codex CLI とそれぞれの認証が必要です。`iro land` は Git と認証済みの `gh` を使い、Codex は起動しません。現在対応する tracker host は `github.com` です。

### 2. iro を install する

現在は、この repository の source checkout から install します。

```bash
git clone https://github.com/r-agatsuma/iro.git
cd iro
go install ./cmd/iro

command -v iro
iro version
```

install 先は `GOBIN`、未設定なら通常 `$(go env GOPATH)/bin` です。`command -v iro` で見つからない場合は、その directory を `PATH` に追加してください。

### 3. 対象 repository を初期化する

対象は、すでに Git repository として作成され、GitHub remote が設定されている必要があります。`iro init` は Git repository や remote を新規作成しません。

```bash
cd /path/to/target-repository
iro init
```

`iro init` は repository root に `iro.toml` と `WORKFLOW.md` を作ります。内容を確認し、通常の repository の手順で記録してください。`iro init` 自身は commit や push を行いません。後続の `iro run` は clean な default branch checkout から実行します。

準備を read-only で確認できます。

```bash
iro doctor
```

### 4. Issue を作り、最初の lifecycle を実行する

GitHub で、背景、実現したい結果、対象外、完了条件が分かる Issue を作ります。ここでは Issue が `#123`、`iro run` が作成した PR が `#456` だったとします。

```bash
# GitHub で Issue #123 を作る

iro run 123
# 出力された PR number を確認する: Created PR #456

# 必要なら独立 review を行う
iro review 456

# finding があれば同じ PR を修正し、もう一度 review する
iro revise 456
iro review 456

# Human が PR を確認し、merge を明示的に許可する
iro land 456

# 次の iro run の前に、local default branch を remote と同期する
git pull

# 必要なら local workspace を確認して片付ける
iro status
iro cleanup 123
```

`review` と `revise` は任意です。`run` が表示した PR number を後続の `review`、`revise`、`land` に渡します。`land` は local branch を同期しないため、成功後は次の作業前に Human が同期します。`cleanup` は merge とは別の任意操作です。

## Why Issue / Pull Request based?

iro が採用するのは、**Issue / Pull Request ベースの開発フロー**です。GitHub を例にすると、それぞれの役割は次のようになります。

| 要素 | 役割 |
|---|---|
| Issue | やること、背景、完了条件を置くチケット |
| Pull Request | 実際の変更を提案し、内容や test 結果を確認・review する場所 |
| Merge | 確認済みの変更を repository へ正式に取り込む操作 |
| GitHub | Issue、PR、review、merge を扱える SaaS の一例 |

chat だけで作業を完結すると、task specification、implementation、review history が別々の session や会話へ分散しやすくなります。Issue に「何を、なぜ、どこまで行うか」を置き、PR に変更と review を残せば、担当者や AI session が変わっても durable な work history をたどれます。

これは Git の branch や commit を学ぶための tutorial ではありません。また、チケット駆動開発（TiDD）や GitHub Flow と iro を同一視するものでもありません。作業と変更を追跡可能にするという点では近い考え方がありますが、iro の具体的な operation と責任境界は独自に定義されています。

## Typical workflow in detail

### 1. Install / prerequisites

- **いつ使うか:** iro を初めて使うとき、または source 更新後に binary を更新するとき。
- **入力:** この repository の source checkout。Go 1.22 以上、Git、`gh`、Codex CLI を事前に用意します。
- **出力:** `PATH` から実行できる `iro` binary。`iro version` は project 外でも実行できます。
- **Human の次の操作:** 対象 repository へ移動し、`iro init` を実行します。古い binary が選ばれていないかは `command -v iro` と `iro version` で確認します。

```bash
go install ./cmd/iro
command -v iro
iro version
```

### 2. `iro init`

- **いつ使うか:** 既存の Git repository を managed iro project として準備するとき。
- **入力:** Git repository 内から、引数なしで実行します。
- **出力:** repository root の `iro.toml` と `WORKFLOW.md`。既存 file は上書きしません。
- **Human の次の操作:** 両 file を確認・調整し、repository の通常の手順で記録します。`iro doctor` で環境と認証を確認し、`run` の前に default branch checkout を clean にします。
- **主な option:** ありません。`--force` もありません。

```bash
iro init
iro doctor
```

### 3. Write an Executable Issue

Executable Issue とは、単に template の見出しを埋めた文章ではありません。

> 背景を知らない別の implementer / AI worker に渡しても、追加の product / architecture decision なしに、同じ externally observable behavior へ収束できる work item

基本構成には、次の4項目が使えます。

```text
Current state
Target state
Non-goals
Acceptance Criteria
```

例えば、次の依頼は実装者が判断しなければならない範囲が広すぎます。

```text
曖昧:
  ログ周りをいい感じに直す
```

同じ依頼でも、観測可能な結果と変更しない範囲まで書くと、worker が実装へ進めます。

```text
Executable:
  Current state:
    timeout時にexit codeだけが表示され原因が分かりにくい

  Target state:
    timeoutしたcommand名とtimeout秒数をstderrに表示する

  Non-goals:
    retry機能は追加しない

  Acceptance Criteria:
    - command名が表示される
    - timeout秒数が表示される
    - exit statusの既存contractは変えない
```

重要なのは見出しの形式ではなく、複数の正解が生まれる未決の externally observable behavior を残さないことです。worker に product / architecture decision を委ねる必要があるなら、実装前に Human がその判断を追加します。

existing behavior や policy を変更する Issue では、必要に応じて現在の [runtime behavior specification](docs/behavior.md) と Git history、最後に変更した PR / Issue を確認してください。現在の実装だけから、過去の制約や失われた design intent を推測して仕様化しないようにします。

### 4. `iro run`

- **いつ使うか:** Executable Issue の実装を fresh Author worker に依頼するとき。
- **入力:** positive な Issue number。managed mode では、clean な default branch checkout、valid な project files、configured GitHub remote が必要です。
- **出力:** Issue ごとの worktree と branch で Codex が作業し、成功すると iro が commit、push、通常の open PR 作成まで行います。stdout に作成した PR number が表示されます。
- **Human の次の操作:** GitHub で PR の差分、test 結果、Author report を確認します。必要なら `iro review`、問題が明確なら `iro revise` へ進みます。
- **主な option:** `--model` / `-m`、`--reasoning-effort`。`--no-sandbox` は advanced option です。

```bash
iro run <issue-number>
```

`run` は local default branch を自動で fetch / pull しません。開始前の同期と checkout の選択は Human が行います。

### 5. `iro review`

- **いつ使うか:** completed implementation を fresh Reviewer worker に独立評価させたいとき。任意の advisory operation です。
- **入力:** `run` が作成した PR number。managed mode では、default branch を base とし、同じ repository の Issue を native closing relation で1件参照する open PR を扱います。
- **出力:** review report 全体を target PR の conversation comment として投稿します。source branch や implementation は変更しません。
- **Human の次の操作:** PR comment の `PASS` / `FINDING` と内容を読み、自分で判断します。command の成功は `PASS` や merge authorization を意味しません。
- **主な option:** `--model` / `-m`、`--reasoning-effort`。`--no-sandbox` は advanced option です。

```bash
iro review <pr-number>
```

### 6. `iro revise`

- **いつ使うか:** PR feedback や finding を反映し、既存の delivery PR を更新するとき。
- **入力:** 修正対象の PR number。managed mode では、iro の canonical Issue branch / relation を満たす open PR が対象です。
- **出力:** fresh Author worker が Issue と PR feedback を読み、成功すると同じ branch へ commit / push して同じ PR を更新します。新しい PR は作りません。
- **Human の次の操作:** 更新された差分と checks を確認し、必要なら再び `iro review` を実行します。
- **主な option:** `--model` / `-m`、`--reasoning-effort`。`--no-sandbox` は advanced option です。

```bash
iro revise <pr-number>
iro review <pr-number>
```

### 7. `iro land`

- **いつ使うか:** Human が PR の内容と merge を最終確認し、その PR を取り込むと決めたとき。
- **入力:** merge を許可する PR number。`iro land <pr-number>` という invocation 自体が Human の明示的な authorization です。
- **出力:** repository policy と対象を検証し、validated PR HEAD を normal merge commit で merge します。成功すると merge 結果と local sync の案内を表示します。
- **Human の次の操作:** local default branch を remote と同期します。AI review の有無や verdict は Land の authorization ではありません。
- **主な option:** managed mode に worker option はありません。advanced な `--unmanaged` form は後述します。

```bash
iro land <pr-number>
```

`land` は local cleanup、local branch の同期、remote branch の明示的削除を行いません。merge が失敗または結果不明の場合も、自動 retry や別方式への fallback は行いません。

### 8. Local sync

- **いつ使うか:** `land` 成功後、次の `iro run` を始める前。
- **入力:** Human が選んだ local default branch checkout。
- **出力:** merge 済みの remote default branch と同期した local checkout。
- **Human の次の操作:** 次の Issue を開始するか、不要な local workspace を cleanup します。

例えば、対象の default branch checkout で次を実行します。

```bash
git pull
```

これは iro が実行する command ではありません。repository の運用に合う同期方法を Human が選びます。

### 9. `iro cleanup` / `iro status`

- **いつ使うか:** managed Issue workspace の local state を確認するとき、または不要になった local resource を片付けるとき。
- **入力:** `status` は引数なし。`cleanup` は特定の Issue number を指定するか、省略して現在の repository の候補をまとめて処理します。
- **出力:** `status` は owned workspace を `CLEAN` / `DIRTY` / `BROKEN` で read-only 表示します。`cleanup` は ownership と安全条件を確認できる worktree、local branch、mapping だけを削除します。
- **Human の次の操作:** `DIRTY` / `BROKEN` は内容と診断を確認し、必要な変更を保存してから recovery 手順を選びます。
- **主な option:** `--force` はありません。Issue / PR / remote branch は確認も変更もしません。

```bash
iro status
iro cleanup 123

# 現在の repository の安全に処理できる managed resource をまとめて処理
iro cleanup
```

## Common worker options

`run`、`review`、`revise` では、番号 operand の後ろに次の option を指定できます。option 同士の順序は問いません。

```bash
iro run 123 --model <model> --reasoning-effort <effort>
iro review 456 -m <model>
iro revise 456 --reasoning-effort <effort>
```

- `--model <model>` / `-m <model>` は、指定した model をその operation の Codex invocation へ渡します。
- `--reasoning-effort <effort>` は、指定した reasoning effort を独立した Codex configuration override として渡します。
- 未指定の項目は Codex 自身の configuration / default selection に委ねます。
- iro は model / effort の catalog、compatibility、fallback を管理せず、両者を synthetic model name に結合しません。指定値を Codex が受け付けなければ worker failure になります。
- `iro.toml` に model / reasoning effort の default はありません。

`--no-sandbox` も同じ3 command で使えますが、通常の Quick Start には不要です。外側の container / VM などで isolation を用意した場合にだけ Human が operation ごとに明示します。

```bash
iro run 123 --no-sandbox
```

## Tips / Advanced usage

### `iro.toml`

`iro.toml` は、iro 自身が managed repository をどう扱うかを定める project configuration です。現在の schema は `iro init` が生成する次の内容だけです。

```toml
version = 1

[tracker]
type = "github"
remote = "origin"

[agent]
type = "codex"

[workspace]
strategy = "git-worktree"
```

現在は `version = 1`、GitHub tracker、Codex agent、Git worktree strategy だけを support します。`tracker.remote` は managed operation が repository identity を解決する remote 名です。別 remote への暗黙の fallback はありません。exact schema と semantics は [runtime behavior specification](docs/behavior.md) を参照してください。

### `WORKFLOW.md`

`WORKFLOW.md` は、worker がその repository で作業するときの project-specific policy / guidance です。build、test、validation、変更してはいけない領域、作業報告の形式などを Human が記述できます。

これは iro core の Git / GitHub authority や lifecycle invariant を拡張する file ではありません。どの snapshot を policy として使うかも operation ごとに異なります。managed Run / Review は project policy を読み、managed Revise は検証済みの starting PR HEAD にある `WORKFLOW.md` を使います。Land は worker を起動しないため読みません。詳細は [runtime behavior specification](docs/behavior.md) を確認してください。

### Unmanaged mode

unmanaged mode は、`iro.toml` / `WORKFLOW.md` を project configuration / policy として使わず、既存 repository に operation-local に iro を持ち込む escape hatch です。Quick Start の標準 route ではありません。各 invocation は `origin` から対象 repository を解決し、worker operation には built-in の保守的な policy を使います。

```bash
iro run 123 --unmanaged
iro review 456 --unmanaged --issue 123
iro revise 456 --unmanaged --issue 123
iro land 456 --unmanaged
```

Review / Revise では、PR と specification Issue の対応を Human が `--issue` で明示します。managed mode の canonical relation、ownership、cleanup を unmanaged state に一般化しません。`iro status` / `iro cleanup` は unmanaged workspace の管理 command ではありません。precondition、failure 時の retained workspace、各 operation の delivery semantics は [runtime behavior specification](docs/behavior.md) を参照してください。

### Pre-implementation Issue review

実装前に曖昧さが疑われる Issue は、Codex / ChatGPT と `gh`、repository history を使って任意に adversarial review できます。新しい iro command は必要ありません。

```bash
gh issue view 123 --comments
git log --oneline --all -- path/to/relevant-area
```

current specification、関連 code、過去に同じ behavior を変更した PR / Issue も必要に応じて確認し、例えば次の問いを AI に渡します。

> この Issue だけを渡された複数の implementer が、どちらも仕様を満たしながら異なる externally observable behavior を実装できる余地はないか。

AI には、不足する product / architecture decision を埋めさせず、選択肢と影響を整理して `Human decision required` として返させます。Human が判断を Issue に追記してから `iro run` へ進みます。

### Recovery / inspection

`iro doctor` は executable、認証、project files、configured repository などを read-only で診断します。`iro status` は managed local workspace の機械状態だけを表示し、Issue や PR の進捗・完了を推測しません。

failed Run、retained workspace、partial state、dirty cleanup、古い binary などの recovery は [operator cookbook](docs/cookbook.md) を参照してください。iro は Human 所有の変更を自動で reset / stash / clean しません。

### Isolation

worker は通常、workspace 内への書き込みに限定した sandbox、approval policy `never`、network enabled で実行されます。sandbox は完全な security boundary や credential / network isolation を意味しません。

`--no-sandbox` は Codex の filesystem sandbox をその invocation だけ無効にします。外側の container / VM と権限境界を Human が確認した場合に限って使ってください。OS、container、network、Codex のその他の policy まで解除する option ではありません。normative な設定は [runtime behavior specification](docs/behavior.md) に定義されています。

## What iro does / does not do

iro の automation は Human が明示した operation の範囲に限られます。

- Human が対象 Issue / PR の選択、product / architecture の仕様判断、最終 merge judgment を所有します。
- Author / Reviewer worker は fresh かつ disposable で、session を durable state として resume しません。
- iro は operation ごとに必要な branch / worktree 準備、commit、push、PR / comment 作成、明示的に許可された merge、安全を確認できる local cleanup を担います。
- daemon、scheduler、unbounded な Review -> Revise loop、Human の `iro land` なしの automatic merge loop は提供しません。
- failed または結果が曖昧な remote mutation を自動 retry / rollback / repair しません。診断を確認し、remote state を確かめた Human が次の操作を選びます。

## References

- [`docs/behavior.md`](docs/behavior.md): 現在の runtime behavior を定義する唯一の normative specification
- [`docs/cookbook.md`](docs/cookbook.md): failure recovery と operator guidance
- [`docs/architecture.md`](docs/architecture.md): 現在の architecture の non-normative な説明
- [OpenAI Symphony](https://github.com/openai/symphony): design inspiration の一つ。iro は独立 project であり、Symphony の dependency、fork、または OpenAI の公式実装ではありません。
