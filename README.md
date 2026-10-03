# iro

`iro` は、GitHub Issue を作業指示、Pull Request（PR）を変更内容の確認・レビュー単位として扱い、Codex ワーカーによる実装を GitHub 上の変更履歴へ接続する CLI である。

Issue に「何を、なぜ、どこまで変更するか」を残し、PR に実装・レビュー結果を残す。必要に応じて別の Codex ワーカーでレビューと修正を行い、最終的なマージは利用者が `iro land` を明示的に実行して決定する。Codex のセッション自体を長期状態として扱わず、作業の根拠と結果をリポジトリと Issue / PR に残すことを基本とする。

```text
GitHub Issue
  -> iro run
  -> Pull Request
  -> optional: iro review -> iro revise -> iro review
  -> Human judgment
  -> iro land
  -> merge

必要なら後で:
  iro status
  iro cleanup
```

## Quick Start

ここでは、既存の Git リポジトリへ iro を導入し、1件の Issue を実装して PR をマージするまでを順に示す。

### 1. Prerequisites を確認する

iro 自体のビルドには Go 1.22 以上を使用する。Go が未導入であれば、公式の [Download and install Go](https://go.dev/doc/install) に従って導入する。

通常の managed workflow では、Git、GitHub CLI（`gh`）、Codex CLI と各認証も必要である。Codex CLI の導入方法は [OpenAI の Codex CLI ドキュメント](https://learn.chatgpt.com/docs/codex/cli) を参照する。

```bash
go version
git --version
gh --version
gh auth status --hostname github.com
codex --version
codex login status
```

現在、managed operation が対象とする tracker host は `github.com` である。

なお、`iro init` だけは Git リポジトリと `git` executable があれば実行できる。GitHub remote、`gh`、Codex、network access は `iro init` 自体の前提ではない。Codex を使う `run` / `review` / `revise` では GitHub access に加えて Codex CLI と Codex authentication が必要である。managed Run は後述の experimental Copilot も選択できる。`land` は GitHub access を必要とするが agent を起動しない。

### 2. iro を install する

次のコマンドで公開 module からインストールする。リポジトリの clone は不要で、利用者の Go toolchain が source から executable をビルドする。

```bash
go install github.com/r-agatsuma/iro/cmd/iro@latest
```

`go install` が配置する binary は、Go の binary directory に入る。通常は system-wide な `/usr/local/bin` へ直接入るわけではない。

配置先は次で確認できる。

```bash
go env GOBIN
go env GOPATH
```

`GOBIN` が設定されていればその directory、空であれば通常 `$(go env GOPATH)/bin` が配置先である。現在の shell から `iro` が見つからない場合は、その directory を `PATH` に追加する。

```bash
GOBIN="$(go env GOBIN)"
if [ -z "$GOBIN" ]; then
  GOBIN="$(go env GOPATH)/bin"
fi
export PATH="$GOBIN:$PATH"

command -v iro
iro version
```

恒久的に利用する場合は、利用している shell の設定ファイルへ同等の `PATH` 設定を追加する。iro は shell 設定を変更しない。

開発時や source checkout から導入したい場合は、補助的な方法として次も利用できる。配置先と `PATH` の設定は上記と同じである。

```bash
git clone https://github.com/r-agatsuma/iro.git
cd iro
go install ./cmd/iro
```

### 3. 対象リポジトリを初期化する

対象は既存の Git リポジトリである必要がある。`iro init` は `git init`、remote 追加、GitHub リポジトリ作成を行わない。

```bash
cd /path/to/target-repository
iro init
```

成功するとリポジトリ root に次を作成する。

```text
iro.toml
WORKFLOW.md
```

- `iro.toml`: iro 自身が managed operation で利用する project configuration
- `WORKFLOW.md`: Codex ワーカーへ与えるリポジトリ固有の作業方針

既存ファイルは上書きしない。

managed `iro run` を使う場合、この2ファイルを確認・編集したうえでリポジトリの通常の手順で履歴へ記録し、利用する named source branch を clean にして、local HEAD を configured remote の同名 branch tip と一致させる。source は既定ブランチでなくてもよい。

既定ブランチへ直接 commit / push できるリポジトリであれば、例えば次のように行う。

```bash
git add iro.toml WORKFLOW.md
git commit -m "Configure iro"
git push
```

既定ブランチへの直接 push を禁止しているリポジトリでは、通常の PR 手順でこの2ファイルを取り込み、その後ローカルの既定ブランチを同期する。iro は repository rule を迂回しない。

準備後は `iro doctor` で環境、認証、project configuration を read-only に確認できる。

```bash
iro doctor
```

### 4. Executable Issue を作る

GitHub に Issue を作成する。iro では、Issue を Codex ワーカーへ渡す task specification として利用する。

最低限、次が分かる状態を目指す。

```text
Current state
Target state
Non-goals
Acceptance Criteria
```

重要なのは見出し自体ではなく、別の実装者が読んでも追加の product / architecture decision を行わず、同じ観測可能な振る舞いへ収束できることである。

詳細は [Writing an Executable Issue](#writing-an-executable-issue) を参照する。

### 5. Issue を実装して PR を作る

Issue が `#123` であるとする。

```bash
iro run 123
```

成功すると、Codex ワーカーの変更を iro が commit / push し、通常の open PR を作成する。出力された PR number を後続操作で使用する。

以下では PR が `#456` だったとする。

### 6. 必要なら独立レビューと修正を行う

レビューは任意である。

```bash
iro review 456
```

レビューで修正すべき点が見つかり、仕様上の判断が追加で不要であれば、同じ PR を更新する。

```bash
iro revise 456
iro review 456
```

`iro review` の出力は advisory なレビュー報告である。command の成功や `PASS` という文字列自体はマージ承認ではない。

### 7. 利用者が確認してマージする

PR の差分、テスト結果、レビュー、リポジトリの状況を利用者が確認し、マージすると判断した場合に実行する。

```bash
iro land 456
```

`iro land <pr-number>` という明示的な invocation 自体を、その PR に対する利用者の merge authorization として扱う。

### 8. 次の作業に備えてローカルを同期する

`iro land` はローカルの checkout を更新しない。次の managed `iro run` の前に、利用する named source branch checkout を configured remote の同名 branch と同期する。既定ブランチを使う場合も同じ条件である。

例えば:

```bash
git pull
```

実際の同期方法はリポジトリの運用に合わせて利用者が選択する。

### 9. 必要ならローカル resource を確認・削除する

```bash
iro status
iro cleanup 123
```

現在の local repository で発見した iro resource をすべて破棄する場合は operand を省略できる。未保存変更や unpublished commits も削除対象となる。

```bash
iro cleanup
```

cleanup はマージとは独立した local lifecycle operation である。GitHub Issue / PR や remote branch を変更しない。

## Why Issue / Pull Request based?

iro が前提とするのは、Issue と Pull Request を中心に作業を追跡する開発フローである。

GitHub を例にすると、各要素の役割は次のように整理できる。

| 要素 | iro での主な役割 |
|---|---|
| Issue | 作業の背景、要求、対象外、受け入れ基準を残すチケット |
| Pull Request | 実際の変更、検証結果、レビューを確認する変更提案 |
| Merge | 確認済みの変更をリポジトリへ取り込む操作 |
| GitHub | Issue / PR / review / merge と変更履歴を扱う現在の tracker / hosting service |

チャットだけで作業を進めると、「何を依頼したか」「なぜその仕様になったか」「何を変更したか」「誰が何をレビューしたか」が、会話、ローカル作業環境、担当者の記憶へ分散しやすい。

Issue を作業指示の正本、PR を変更確認の単位とすれば、担当者や Codex セッションが変わっても、要求と変更履歴をリポジトリ側から追跡しやすい。iro はこの流れのうち、Codex ワーカーの起動、worktree、commit、push、PR / review comment、明示的に許可された merge などを command 単位で支援する。

この説明は Git の tutorial ではない。また、「チケット駆動開発（TiDD）」や「GitHub Flow」と iro を同義語として扱うものでもない。作業単位と変更履歴を追跡可能にするという点で近い考え方はあるが、iro の operation と authority boundary は iro の runtime contract として独自に定義する。

## Command reference

現在の CLI surface は次のとおりである。

```text
iro version
iro init
iro doctor
iro status
iro run <issue-number> [--unmanaged] [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]
iro review <pr-number> [--unmanaged --issue <issue-number>] [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]
iro revise <pr-number> [--unmanaged --issue <issue-number>] [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]
iro land <pr-number> [--unmanaged]
iro cleanup [<issue-number>]
```

worker option は番号 operand の後ろへ置く。unsupported option、重複 option、余分な operand は usage error とする。

以下では、通常の managed workflow を中心に説明する。`--unmanaged` は [Unmanaged mode](#unmanaged-mode) にまとめる。

### `iro version`

```text
iro version
```

Git リポジトリ、project file、外部 command、認証を必要とせず、現在実行されている iro binary の build 情報を表示する。

source 更新後に古い binary を参照していないか確認する場合は、次も併用する。

```bash
command -v iro
iro version
```

### `iro init`

```text
iro init
```

既存 Git リポジトリを managed iro project として初期化する local scaffold operation である。

必要なのは `git` executable と Git リポジトリだけである。Git remote、GitHub authentication、Codex は要求しない。

新規作成するのは `iro.toml` と `WORKFLOW.md` の2ファイルであり、commit / push、remote 作成、認証、既存ファイルの上書きは行わない。`--force` はない。

両ファイルの片方だけが存在する、または既存 `iro.toml` が invalid である場合は自動 repair せず failure とする。

### `iro doctor`

```text
iro doctor
```

現在の environment と project state を read-only で診断する。

可能な範囲で、Git、project files、configured remote、GitHub CLI context、GitHub authentication、Codex executable / authentication、各 executable の path / version などを確認する。一つの failure だけで残りの独立した診断を打ち切らない。

`iro doctor` は file、Git state、GitHub state、Codex authentication state を変更しない。

### `iro run`

```text
iro run <issue-number> [--unmanaged] [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]
```

managed form の典型形:

```bash
iro run 123
```

Executable Issue を fresh な Author ワーカーへ渡し、実装を PR として delivery するときに使う。

managed Run は概ね次を要求する。

- valid な `iro.toml` と `WORKFLOW.md`
- configured GitHub repository と認証
- clean な invoking checkout
- invoking checkout が named branch `B` で、local HEAD が configured remote の実際の `B` tip と一致すること
- 対象 Issue が readable であること
- 今回の delivery ref / workspace が local / remote で衝突しないこと

iro は検証済みの source HEAD から fresh な delivery branch / worktree を作成し、workspace の `WORKFLOW.md` に従う Codex Author を起動する。成功後、変更を commit / push し、`iro/issue-N-D` を head、source branch `B` を base とする通常の open PR を作成する。`D` は invocation ごとの新しい delivery ID であり、同じ Issue の後続 Run も別 delivery になる。以前の branch / workspace / PR を再利用しない。

PR body は固定の `Closes #N` を含み、Author report は別の PR comment に残す。managed Review / Revise は現在の raw body 内の local Issue token を typed validation して specification Issue を解決する。native closing metadata や branch の Issue number はその authority にしない。merge 時の Issue closure は GitHub の動作に委ね、Land の成功条件に使わない。

source branch は既定ブランチでなくてもよい。iro は開始前に自動 fetch / pull / push で source checkout を同期しない。利用者が同期する。

worker failure や delivery failure では、Human の変更を自動 reset / clean / stash / rebase して修復しない。診断と retained state を確認してから次の操作を選択する。

### `iro review`

```text
iro review <pr-number> [--unmanaged --issue <issue-number>] [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]
```

managed form の典型形:

```bash
iro review 456
```

open PR を fresh な Reviewer ワーカーで独立評価し、Reviewer の final response 全体を PR comment として投稿する advisory operation である。source branch や implementation を変更しない。

managed Review は `iro run` が作成した PR に限定しない。Human が作成した PR や fork head の PR も、次の条件を満たせば対象になり得る。

- PR が open である
- 開始時の current raw body が、同じ repository の readable Issue ちょうど1件へ解決できる
- PR の base / HEAD OID と必要な context が検証でき、workspace の HEAD が開始時の PR HEAD と一致する

Draft PR も Review の対象にできる。

target の local branch、Issue worktree、ownership mapping、PR creator identity、default base、native closing relation、他の active delivery の一意性は eligibility に要求しない。worker policy は invocation 側の `WORKFLOW.md` を使う。

Reviewer は開始時に検証した PR snapshot を disposable workspace で評価する。review comment は Human-facing text として扱い、iro は `PASS` / `FINDING` を runtime control signal として parse しない。`FINDING` が含まれていても command 自体は成功し得る。

後から PR body を編集すると、次の managed Review / Revise は新しい specification Issue を解決する。実行中の Issue / HEAD binding は変更しない。

### `iro revise`

```text
iro revise <pr-number> [--unmanaged --issue <issue-number>] [--model <model> | -m <model>] [--reasoning-effort <effort>] [--no-sandbox]
```

managed form の典型形:

```bash
iro revise 456
```

既存 PR の feedback を fresh な Author ワーカーへ渡し、同じ PR を更新するときに使う。

managed Revise は configured repository の open PR を選び、current raw body から specification Issue を解決する。head repository は同じ repository、remote head ref の tip は開始時の PR HEAD `H1` と一致する必要がある。default base、canonical head name、native closing relation、他の active PR / shared head の一意性を要求しない。fork の Revise は対象外である。

PR creator identity や iro-created marker は要求しない。Human-created / unmanaged-produced PR も自身の operation 条件だけで判断し、以前の producer の local state や policy を採用しない。

Author は origin Issue と PR metadata / diff / comments / reviews / checks を読み、成功すると新しい commit を同じ branch へ通常 push して同じ PR を更新する。replacement PR は作らない。

`iro/*` head は exact ref の clean / H1 と一致する registered worktree を再利用でき、なければ H1 から新規 materialize する。Human branch は毎回 fresh detached workspace で扱い、Human checkout / local branch を変更しない。v1 ownership JSON は参照・作成・移行しない。

managed Revise の worker policy は、検証済み starting PR HEAD に含まれる `WORKFLOW.md` をその invocation の固定 policy として使う。invoking checkout の `WORKFLOW.md` は Revise の worker policy ではない。

H1 を sole parent とする local commit の検証後、push 直前に一度だけ OPEN / same head repository・ref / remote tip / push destination を再検証する。drift、削除、closed、読取不能、destination change は commit を保持して push せず停止する。body-only / base-only の変更は実行中の rebind / abort 条件にしない。final read と通常 push の間の race は残り、force / lease / lock / 自動 repair / retry は行わない。

### `iro land`

```text
iro land <pr-number> [--unmanaged]
```

managed form:

```bash
iro land 456
```

利用者が選択した PR を通常の merge commit でマージする operation である。`iro land <pr-number>` の invocation 自体を、その PR に対する merge authorization として扱う。

managed Land は worker を起動しないため Codex を要求せず、`WORKFLOW.md` を読み取らない。`iro.toml` から repository identity / configuration を解決する。

主な managed precondition は次のとおりである。

- PR が open かつ non-Draft
- PR が configured repository に存在し、有効な exact HEAD を取得できる
- repository が normal merge commit を許可
- 実行者に必要な repository permission がある
- mergeability、required checks / reviews、merge queue 等の policy が immediate merge を許可

managed Land は fork head も扱い、fork の write permission や push 条件を要求しない。origin Issue、native closing relation、default base、canonical head、他の active PR の一意性を検査せず、PR body の origin resolver を実行しない。unmanaged Land は同じ origin repository の head という従来の制約を維持する。

AI Review の実行有無、Reviewer verdict、PR creator identity、local Issue worktree の有無は Land authorization ではない。

validation で得た exact PR HEAD を merge request に bind し、HEAD drift があれば failure とする。admin bypass、force merge、branch auto-update、別 merge method への fallback、自動 retry は行わない。

成功出力は PR number と merge commit OID を示し、origin Issue を主張しない。Issue close / reopen API は呼ばず、GitHub native behavior による Issue closure の有無を成功条件にしない。成功後も local checkout、Issue worktree、local branch、ownership mapping、remote branch を自動 cleanup しない。

### Local sync

`iro land` 成功後、次の managed `iro run` の source branch は clean かつ local HEAD が remote の同名 branch tip と一致する必要がある。成功出力の default branch 同期 hint は典型例であり、Run の source / PR base を既定ブランチに限定しない。

```bash
git pull
```

これは informational example であり、iro が実行する command ではない。repository の運用に適した同期方法を利用者が選択する。

### `iro status`

```text
iro status
```

現在の local repository の `iro/*` branch と attached registered worktrees、および登録済みの iro runtime workspace を read-only で表示する。branch、worktree path(s)、local HEAD OID を示し、detached residue も runtime path と認識できる場合に含める。

cleanliness や GitHub Issue / PR の semantic state は判定しない。Status は Cleanup の安全性や復元可能性を保証しない。

`iro.toml`、`WORKFLOW.md`、remote configuration、tracker / agent の認証・network・実行環境、v1 ownership JSON は要求・参照しない。

### `iro cleanup`

```text
iro cleanup [<issue-number>]
```

特定 Issue の local iro resource を best-effort で破壊的に削除する。`iro/issue-N` と `iro/issue-N-*` の branch、その attached worktrees、一意に N を示す登録済み runtime workspace が対象となる。Issue 7 と 70 は区別する。

```bash
iro cleanup 123
```

operand を省略した場合は、現在の local repository で発見した iro resource をすべて処理する。Issue に結び付かない登録済み runtime detached residue も含める。

```bash
iro cleanup
```

Git worktree の force removal、local branch の force deletion、既知の正確な runtime workspace path に限った残骸削除を試行する。dirty / untracked / ignored state、unpushed commits、祖先関係、remote recoverability による保護は行わない。remote branch、Issue、PR は確認・変更せず、v1 ownership JSON も参照・削除しない。

失敗しても独立した残りの処理を続け、成功済み削除を rollback しない。残存・観測不能な target や mechanism failure があれば non-zero となる。arbitrary filesystem scan / path resemblance による削除や自動 unlock / prune / repair は行わず、Git 登録を失った filesystem-only residue の発見は保証しない。

## Writing an Executable Issue

iro では Issue を単なるメモではなく、1回の worker operation に渡す task specification として扱う。

Executable Issue の目安は次である。

> 背景を知らない複数の実装者へ同じ Issue を渡したとき、追加の product / architecture decision なしに、外部から観測できる振る舞いが同じ方向へ収束する。

よく使う構成は次である。

### Current state

現在何が起きているかを書く。再現条件、現在の出力、既存仕様、確認済みの制約など、実装者が出発点を再確認できる情報を置く。

### Target state

変更後に外部から何が観測できるべきかを書く。「内部をきれいにする」ではなく、利用者、CLI、API、file、GitHub state 等から確認できる振る舞いを優先する。

### Non-goals

今回決めないこと、変更しないことを書く。隣接機能を「ついでに」設計し直すことを防ぐ。

### Acceptance Criteria

実装後に yes / no で確認できる条件を書く。implementation detail の指定ではなく、要求を満たしたかを検証できる基準にする。

例えば次の依頼は曖昧である。

```text
ログ周りをいい感じに直す。
```

例えば次のように、観測可能な結果と対象外を明示する。

```text
## Current state

外部 command が timeout した場合、exit code だけが表示され、
どの command が何秒で timeout したか分からない。

## Target state

timeout failure の stderr に、
- command 名
- configured timeout 秒数
を表示する。

## Non-goals

- automatic retry は追加しない。
- timeout 値の設定方法は変更しない。

## Acceptance Criteria

- timeout した command 名が stderr から確認できる。
- configured timeout 秒数が stderr から確認できる。
- timeout 時の exit status contract は変更しない。
- existing non-timeout failure の behavior を変更しない。
```

見出しの有無そのものを runtime が parse するわけではない。重要なのは、実装者が新しい仕様判断を創作しなくても次の reviewable checkpoint まで進めることである。

### 既存 behavior を変更する場合

既存の behavior / policy を変更する Issue では、現在のコードだけから過去の設計意図を推測しない。

必要に応じて次を確認する。

- [runtime behavior specification](docs/behavior.md)
- target code / document の Git history
- その箇所を導入・最後に変更した PR
- origin Issue や関連 Issue の Target state / Non-goals / Acceptance Criteria
- current tests

履歴にしか残っていない重要な判断が見つかった場合は、今回の Issue へ必要な判断を durable に書き戻してから実装する。

### 実装前に ambiguity review を行う

Issue が十分に executable か不安な場合、専用の `iro inspect` command は必要ない。Codex / ChatGPT と `gh`、repository history を使って任意に事前レビューできる。

```bash
gh issue view 123 --comments
git log --oneline --all -- path/to/relevant-area
```

例えば AI へ次の問いを与える。

> この Issue だけを渡された複数の実装者が、どちらも仕様を満たしながら異なる外部挙動を実装できる余地を列挙してください。不足する product / architecture decision を補完せず、Human decision required として返してください。

AI の役割は不足仕様を勝手に埋めることではなく、複数の合理的解釈が残る箇所を発見することである。

## Common worker options

`run` / `review` / `revise` は、番号 operand の後ろに worker configuration option を指定できる。

### `--model <model>` / `-m <model>`

その invocation で選択した runtime へ requested model を渡す。managed Copilot Run は native `--model` を使い、Codex catalog では検証しない。requested value は resolved runtime provenance ではない。

```bash
iro run 123 --model <model>
iro review 456 -m <model>
```

model 名は iro が catalog から選択・検証するものではない。選択 runtime が受け付けない値であれば worker failure になる。fallback は行わない。

### `--reasoning-effort <effort>`

reasoning effort だけを operation 単位で override する。

```bash
iro run 123 --reasoning-effort <effort>
```

model と reasoning effort は独立した option であり、iro は synthetic model name を生成しない。managed Copilot Run は native `--reasoning-effort` を使い、`none` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` を受け付ける。それ以外は副作用前に拒否し、model / effort pair の runtime rejection は failure とする。

### `--no-sandbox`

`run` / `review` / `revise` の当該 invocation だけ Codex の filesystem sandbox を無効化する advanced option である。

```bash
iro run 123 --no-sandbox
```

通常の Codex worker は sandbox を使用する。Copilot では `--no-sandbox` を workspace 作成・worker 起動・remote mutation 前に拒否する。Copilot の permission flag へ変換しない。`--no-sandbox` は、container / VM 等で外側の isolation と権限境界を用意している場合や、sandbox が実行環境と干渉する場合に利用者が明示的に選択する。

この option は OS、container、network、Codex のその他の policy を解除するものではない。

### option grammar

worker option は operand より後ろに置く。同じ option の重複、empty value、unsupported extra argument は usage error となる。

model / reasoning effort を省略した場合、その項目の選択は選択 runtime の configuration / default に委ねる。`iro.toml` に worker model default は保持しない。

## Tips / Advanced usage

### `iro.toml`

`iro.toml` は managed iro project の configuration file である。`iro init` が生成する現在の schema は次である。

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

通常の組み合わせは version 1、GitHub tracker、Codex agent、Git worktree strategy である。managed GitHub Run / Author に限り、以下の experimental Copilot selection も利用できる。`iro init` の生成値は引き続き Codex である。

`tracker.remote` は managed operation が repository identity を解決する Git remote 名である。別 remote を推測して fallback しない。

model / reasoning effort、retry policy、merge authorization 等をこの file に暗黙保存しない。

exact schema と operation ごとの normative semantics は [docs/behavior.md](docs/behavior.md) を参照する。

### Experimental Copilot Run

managed project の `iro.toml` で次を指定する。

```toml
[agent]
type = "copilot"
```

対応するのは **managed GitHub Run / Author のみ**。Review / Revise は worker / workspace / remote mutation 前に unsupported error となる。Land は agent-independent であり、この config でも実行できる。Status / Cleanup は agent dependency を持たず、unmanaged Run / Review / Revise は config を読まず built-in Codex のままである。Copilot selector を unmanaged に追加しない。

```bash
iro doctor
iro run 123 --model <copilot-model> --reasoning-effort high
```

Human environment の公式 [BYOK/custom-provider](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#environment-variables) 設定を許可する。有料 GitHub-hosted Copilot account はこの connector の実装・テストに必要ない。project environment で GitHub-hosted Copilot service は未 acceptance であり、production support / Codex parity を主張しない。agent / account / provider / model の自動 fallback は行わない。

Copilot CLI 1.0.91 の実行を確認している。exact patch pin はせず、必要な native flags と JSONL completion を検証する。auto-update を disabled にし、stdin の complete task/context と外部 private custom-agent profile の controlling policy を分離する。Human の native config を private home にコピーし、auto routing fallback / memory / hooks / plugins を無効化する。repository hooks / extensions / workspace MCP の有効化環境変数 `GITHUB_COPILOT_PROMPT_MODE_REPO_HOOKS` / `GITHUB_COPILOT_PROMPT_MODE_EXTENSIONS` / `GITHUB_COPILOT_PROMPT_MODE_WORKSPACE_MCP` は子プロセスで明示的に false に上書きする。Human の config / credential は変更せず、session を resume / adopt しない。repository instruction files には Copilot の semantics が適用される。

shell / file tools を制限し、非対話 approval、ask-user / built-in MCP / remote export の無効化、Git / gh deny patterns を使う。これらは **hard OS sandbox ではない**。allowed shell は path / command heuristics を回避でき、Human が許可した development VM と behavioral policy が trust boundary となる。`--no-sandbox` は Copilot で使用できない。

Doctor は Copilot executable / version / invocation flags を read-only で診断する。provider readiness / hosted entitlement は zero-side-effect probe がないため `UNKNOWN` と表示し、Doctor の成功が provider 認証の成功を意味することはない。login / logout や provider request は行わない。

実 CLI の loopback BYOK harness は、有料 account / 実 provider credentials を使わず次で再現できる。

```bash
IRO_TEST_COPILOT_CLI=1 go test ./internal/iro -run TestCopilotLocalBYOKAcceptance -v
```

worker timeout は 30 分。終了コード、非空 final response、JSONL completion を検証し、unknown / malformed output、未完了 tool、tool failure、timeout、CLI が公開する permission / provider rejection は failure とする。観測境界は Copilot CLI であり、CLI が破棄した provider 終了理由を HTTP proxy 等で復元しない。例えば BYOK の `finish_reason=length` / `finish_reason=content_filter` が正常な CLI completion に変換され、理由が出力に残らなければ、iro は成功として扱う場合がある。これは experimental な制約である。Git commit / push / PR create と result / failure comments は既存 Run 同様に iro が所有する。詳細は [Copilot の runtime contract](docs/behavior.md#run-copilot-001-experimental-selection-and-option-matrix) を参照する。

### `WORKFLOW.md`

`WORKFLOW.md` は repository-specific worker policy である。

Human は、例えば次を記述できる。

- build / test / formatter / static analysis の実行方針
- repository 固有の変更制約
- validation 手順
- 作業報告に必要な情報
- Issue scope を超えて変更してはならない領域

`WORKFLOW.md` は iro core が持つ Git / GitHub lifecycle authority を拡張する file ではない。Issue / PR body、comments、AGENTS guidance も同様に、iro core の mutation boundary を勝手に拡張できない。

policy source は operation ごとに同一ではない。

- managed Run: initialized project の policy を使う
- managed Review: invocation-side project policy を使う
- managed Revise: exact starting PR HEAD に含まれる `WORKFLOW.md` を、その invocation の固定 policy として使う
- managed Land: worker を起動しないため `WORKFLOW.md` を読まない

詳細は [docs/behavior.md](docs/behavior.md) を参照する。

### Unmanaged mode

`--unmanaged` は、managed project contract を利用せず、operation-local に iro を持ち込むための escape hatch である。

```text
iro run <issue-number> --unmanaged [worker options...]
iro review <pr-number> --unmanaged --issue <issue-number> [worker options...]
iro revise <pr-number> --unmanaged --issue <issue-number> [worker options...]
iro land <pr-number> --unmanaged
```

unmanaged operation は `iro.toml` を読まず、`WORKFLOW.md` を iro policy として読まない。repository identity は Git remote `origin` から解決し、worker operation は built-in の保守的な policy を使う。

managed branch / worktree を要求・採用せず、unmanaged operation 自身も durable な adoption / ownership state を作らない。Run の body は `Refs #N` とし、後続 managed consumer は自身の current relation / policy / integrity を独立に検証する。

主な用途は、iro project file を導入していない既存リポジトリや、Human が specification Issue を明示したい PR へ、一回の operation 単位で iro を適用することである。

unmanaged は unsafe / force mode ではない。force push、automatic reset / clean / stash / rebase / repair、automatic retry、repository policy bypass を許可しない。

operation ごとの identity、workspace、same-repository 制約、partial failure semantics は [docs/behavior.md](docs/behavior.md) を参照する。

### Isolation

通常の Author / Reviewer ワーカーは Codex sandbox を使う。現在の worker contract では approval policy を自動対話に委ねず、iro の operation boundary の中で作業させる。

sandbox は完全な credential / network security boundary を意味しない。強い隔離が必要な環境では、OS permission、container / VM、network policy 等を別に設計する。

`--no-sandbox` は outer isolation を Human が確認した場合にのみ使う。

### Recovery / inspection

まず read-only な状態確認を使う。

```bash
iro doctor
iro status
iro version
```

failed Run、partial managed state、retained worktree、destructive cleanup、manual filesystem deletion 後の Git worktree registration 等の recovery recipe は [operator cookbook](docs/cookbook.md) にまとめている。

iro は Human 所有の state を「たぶん不要」と推測して自動修復しない。failure diagnostic を読んで resource state を確認してから、保存、手動修復、cleanup、fresh retry のどれを行うか Human が決める。

## What iro does / does not do

iro は autonomous software-development loop ではなく、明示的な operation を組み合わせるためのハーネスである。

iro が担うもの:

- Issue を task specification として Codex Author へ渡す
- operation ごとに必要な worktree / branch を準備する
- worker result を検証後、必要な commit / push / PR update を行う
- fresh Reviewer による advisory review を PR comment として残す
- Human が明示的に選択した PR を repository policy の範囲で merge する
- current local repository で発見した iro resource を明示 invocation により破壊的に cleanup する
- failure 時に自動修復せず診断可能な state を残す

iro が担わないもの:

- product / architecture decision の自動決定
- daemon / scheduler
- unbounded な Review -> Revise loop
- Reviewer text を control signal とした automatic merge
- Human の明示的な `iro land` なしの managed automatic merge
- failed / ambiguous remote mutation の automatic retry
- force push
- Human state の automatic reset / clean / stash / rebase
- repository rule / required review / merge queue の bypass
- Codex session の durable resume
- GitHub Issue の automatic creation / assignment / labeling

Human は repository を直接操作する authority、task specification の最終判断、operation target の選択、merge judgment を保持する。

## References

- [`docs/behavior.md`](docs/behavior.md): 現在の runtime behavior を定義する唯一の normative specification
- [`docs/cookbook.md`](docs/cookbook.md): failure recovery と operator guidance
- [`docs/architecture.md`](docs/architecture.md): 現在の architecture の non-normative な説明
- [OpenAI Symphony](https://github.com/openai/symphony): 設計上の着想源の一つ。iro は独立 project であり、Symphony の dependency、fork、公式配布物、OpenAI の公式実装ではない
