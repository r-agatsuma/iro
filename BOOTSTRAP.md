# BOOTSTRAP.md

## 1. 目的

この文書は、`iro` の bootstrap MVP を最初の Codex に実装させるための一回限りの作業指示書である。

runtime behavior の仕様書ではない。command の precondition、副作用、state transition、Codex invocation、tracker boundary は `docs/behavior.md` を実装すること。

## 2. 参照文書

実装前に以下を最後まで読むこと。

1. `AGENTS.md`
2. `BOOTSTRAP.md`
3. `docs/behavior.md`
4. `docs/architecture.md`

役割は次の通り。

```text
docs/behavior.md
  normative runtime specification

BOOTSTRAP.md
  bootstrap implementation scope / Definition of Done

docs/architecture.md
  non-normative diagrams

AGENTS.md
  repository development principles
```

runtime behavior は `docs/behavior.md` を唯一の正本とする。
architecture の図から未定義仕様を補完してはならない。

## 3. iro とは何か

`iro` は Issue-driven Repository Orchestrator の作業名を持つ汎用 CLI である。

Issue Tracker を durable work state として扱い、Issue ごとの Git worktree を用意し、Codex worker を非対話実行して作業させ、その結果を Issue へ戻す。

MVP はローカル実行・human dispatch を優先する。

```text
Human
  selects Issue / dispatches / reviews / commits / pushes / merges / closes

Issue Tracker
  semantic work state

Git
  implementation artifacts / history

iro
  validates / fetches / creates workspace / invokes Codex / reports

Codex
  disposable implementation worker
```

## 4. Bootstrap MVP の成功条件

次の 3 command が `docs/behavior.md` の仕様どおり動作すること。

```text
iro init
iro doctor
iro run <issue-number>
```

さらに、`iro run <issue-number>` を使って iro repository 自身の次の開発 Issue を 1 件処理し、人間が生成差分を review できる状態に到達できることを self-hosting 移行条件とする。

bootstrap MVP では、人間が以下を行ってよい。

- Issue の起票
- `iro run <issue-number>` の実行
- failed run 後の Git cleanup / stash
- 生成差分の review
- commit
- branch の push
- merge
- Issue の最終 close

これらを自動化しない。

## 5. 前提環境

人間が事前に以下を用意する。

- Debian 系 Linux VM
- Go toolchain
- Git
- Git repository として初期化済みの iro repository
- GitHub remote
- `gh` CLI と authentication
- Codex CLI と authentication

`iro` 自身が不足している外部環境を自動構築してはならない。

ライセンスは Apache License 2.0 とする。

## 6. Repository の初期構成

最低限、以下を想定する。

```text
iro/
├── AGENTS.md
├── BOOTSTRAP.md
├── LICENSE
├── README.md
├── go.mod
├── cmd/
├── internal/
└── docs/
    ├── architecture.md
    └── behavior.md
```

実装に必要な追加 directory は作成してよいが、MVP に不要な framework 構造を先に作らない。

`README.md` には、`iro` が OpenAI Symphony の設計思想に着想を得た独立プロジェクトであることを明記する。fork、公式配布物、OpenAI の公式実装であるかのように表現してはならない。

## 7. 実装対象

### 7.1 `iro init`

`docs/behavior.md` の `iro init` contract を実装する。

最低限、repository root に次を生成できること。

```text
WORKFLOW.md
iro.toml
```

### 7.2 `iro doctor`

`docs/behavior.md` の `iro doctor` contract を実装する。

read-only diagnostic command とし、Git / project files / configured remote / `gh` / GitHub auth / Codex / Codex auth を可能な範囲でまとめて診断する。

### 7.3 `iro run <issue-number>`

`docs/behavior.md` の `iro run <issue-number>` contract を実装する。

`iro` が GitHub Issue I/O と workspace lifecycle を担当し、Codex は取得済み Issue を実装タスクとして受け取る。

Codex invocation の sandbox、approval、network、developer instructions、ephemeral session、Git制約は `docs/behavior.md` に従う。

## 8. 実装境界

MVP では既存 CLI を subprocess として使う。

- Git: `git`
- GitHub: `gh`
- Codex: `codex`

GitHub API client、OpenAI API の直接統合、Codex App Server は使わない。

外部 command 呼び出しはテスト可能な境界へまとめる。

正式な plugin interface は作らない。

## 9. Runtime state

runtime state と run log は対象 repository へ commit しない。

XDG Base Directory に沿う配置を推奨する。

```text
~/.local/state/iro/
~/.local/share/iro/workspaces/
```

Codex thread/session の永続化、thread ID 管理、resume は実装しない。

## 10. Testing

command behavior の期待値は `docs/behavior.md` を正本としてテストする。

最低限、以下を自動テストする。

### `init`

- Git repository 内の clean initialization
- Git repository 外での failure
- remote がなくても init 自体は成功
- existing files を上書きしない
- partial / invalid initialization を変更せず拒否
- generated `iro.toml` が configured remote を含む

### `doctor`

- read-only
- missing dependency を複数まとめて報告
- configured remote missing を報告
- GitHub authentication failure を報告
- Codex executable missing を報告
- Codex authentication failure を報告

### `run`

fake / stub command runner を使い、少なくとも以下を確認する。

- precondition failure 前に branch / worktree を作らない
- Issue fetch failure 時に Codex を起動しない
- configured remote の repository identity を使う
- initial branch が invoking checkout の HEAD commit から作られる
- ownership mapping が一致する clean な既存 Issue worktree は再利用できる
- ownership mapping がない既存 branch/worktree を iro-owned と推測しない
- dirty な Issue worktree は変更せず拒否する
- Codex を Issue worktree で起動する
- Codex invocation が normative flags/config を含む
- Issue payload が Codex task input として渡される
- Codex は commit を前提にしない
- Codex result を target Issue に返す
- unrelated branch / worktree を変更しない

実 GitHub / Codex を必要とする integration test は unit test と分離する。

## 11. Explicit Non-goals

以下は bootstrap MVP に含めない。

- daemon
- automatic dispatch
- parallel workers
- scheduler
- automatic retry policy / retry queue
- `iro resume`
- `iro status`
- `iro cleanup`
- Codex session/thread management
- Codex App Server integration
- direct OpenAI API integration
- Forgejo / Gitea / Linear support
- full tracker plugin framework
- GitHub Projects integration
- automatic Issue creation
- automatic Issue close / reopen
- automatic label / assignee / milestone changes
- automatic PR creation
- automatic commit
- automatic push
- automatic merge
- Web UI
- network-specific domain support
- structured Codex JSONL event parsing

manual cleanup 後に同じ `iro run <issue-number>` を再実行する fresh rerun は MVP に含む。これは session resume ではない。

## 12. Definition of Done

bootstrap MVP は以下をすべて満たしたとき完了とする。

- `go test ./...` が成功する
- `go vet ./...` が重大な問題なく完了する
- `iro init` が `docs/behavior.md` 通り動作する
- `iro doctor` が `docs/behavior.md` 通り動作する
- `iro run <issue-number>` が configured GitHub repository の Issue を取得できる
- Issue 用 branch / worktree を作成し、local ownership mapping を記録できる
- ownership mapping が一致する Issue worktree だけを安全に再利用できる
- dirty workspace を変更せず拒否できる
- Codex authentication を事前検証できる
- Codex を指定された sandbox / approval / network / ephemeral policy で実行できる
- iro-generated developer instructions を Codex へ注入できる
- Issue 内容を Codex の task input として渡せる
- Codex の結果を GitHub Issue へ返せる
- Codex が Git commit / push や tracker mutation を行わない設計になっている
- precondition failure で外部環境を勝手に変更しない
- unrelated Git resources を変更しない
- README に install / bootstrap usage が記述される
- README に OpenAI Symphony に着想を得た独立プロジェクトであることが明記される
- `docs/behavior.md` と実装・テストが一致する
- iro 自身の Issue を 1 件処理する準備が整う

## 13. 実装開始時の指示

実装前に以下を行うこと。

1. 4 文書を最後まで読む。
2. repository の現状を確認する。
3. `docs/behavior.md` と矛盾する既存コードがあれば、変更前に人間へ日本語で報告する。
4. MVP scope を越える機能を追加しない。
5. 小さな実装単位に分ける。
6. precondition、state transition、副作用境界をテストで固定する。
7. `go test ./...` と `go vet ./...` を実行する。
8. Definition of Done と照合し、日本語で結果を報告する。

新しい大きな設計判断が必要になった場合は、推測で scope を広げず、人間へ報告する。
