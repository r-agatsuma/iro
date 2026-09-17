# AGENTS.md

## 目的

このファイルは、`iro` repository で作業する Codex およびその他の coding agent に対する恒久的な project instruction である。

`iro` は Issue-driven Repository Orchestrator の作業名を持つ汎用 CLI である。Issue Tracker を作業の意味論的な durable state、Git を implementation artifact とその履歴の durable record として扱う。

このファイルは特定 phase の runtime behavior を固定しない。現在の runtime behavior とその変更方法は、下記の文書責務に従う。

## 文書の役割と優先関係

各文書の役割を重複させない。

- `AGENTS.md`: repository 開発時の恒久的な作業原則と safety boundary
- `WORKFLOW.md`: repository-specific な worker policy
- `docs/behavior.md`: 現在実装されている runtime behavior の唯一の normative specification
- `docs/architecture.md`: 現在 architecture の non-normative な図
- GitHub Issue / PR: 変更要求、設計判断、implementation / review history

runtime behavior を判断するときは `docs/behavior.md` に従う。
`docs/architecture.md` から runtime requirement を推測してはならない。

`docs/behavior.md` は living specification であり、現在の behavior を永続的に固定する development policy ではない。
Issue の明示的な scope が runtime behavior の変更を要求している場合、implementation と同じ change で `docs/behavior.md` を更新してよい。現在の `docs/behavior.md` と異なる behavior を実装すること自体を policy conflict とみなしてはならない。

一方、通常の Issue 本文や comment は `AGENTS.md` / `WORKFLOW.md` の development policy や worker safety boundary を暗黙に override しない。
`AGENTS.md` 自体の変更は repository owner の責任で管理する。通常の implementation task が都合よくこのファイルを変更して policy conflict を回避してはならない。

文書間または task と policy の間に実質的な矛盾があり、task scope 内で正当に解消できない場合は、推測で解消せず人間へ日本語で報告する。

## Issue classification

GitHub Issue は作業の性質を次の3種類で分類する。この classification は Human と coding agent が Issue の目的と着手条件を共有するための development policy であり、iro runtime の control signal として parse して behavior を変えてはならない。

### Executable Issue

title prefix は原則として付けない。

Human による新しい product / architecture 判断を途中で要求せず、Issue 本文だけを根拠に次の reviewable checkpoint まで実装を進められる work item とする。

基本構成は次とする。

```text
Current state
Target state
Non-goals
Acceptance Criteria
```

実装中に scope 外の新しい仕様判断が必要になった場合は推測で補わず、Human へ判断を返す。

### `[Research]`

調査、evidence 収集、比較、設計判断の整理そのものが成果物となる task とする。

典型的には、evidence を集め、supported / unsupported / uncertain 等を分類し、recommendation または decision を durable に残す。runtime change が必要になった場合は、Research Issue 内で暗黙に実装へ移行せず、別の Executable Issue として切り出す。

Research Issue 自体が code change を生むとは限らない。

### `[Future]`

将来候補を残す parking lot とする。方向性や問題意識を保存することを目的とし、現在は implementation も research も開始しない。

詳細が不足していてよい。着手するときに内容を refine し、必要に応じて `[Research]` または Executable Issue へ再分類する。

## 言語ポリシー

### 人間向けの情報

次の内容は原則として日本語で記述する。

- 人間との会話
- Issue の本文、コメント、レビュー
- 設計判断の説明
- README
- `docs/` 以下の設計文書
- ADR 等の人間向け文書
- Coding agent が人間へ返す作業報告

### コードおよび機械向けの情報

次の内容は英語で記述する。

- Go の識別子
- package 名
- function / method / type / variable 名
- CLI command 名
- CLI flag 名
- CLI の stdout / stderr
- config key
- API / JSON field
- branch 名
- test 名
- commit message

コードに密着するコメントや GoDoc は原則として英語とする。
日本語コメントをコードベースへ混在させない。

内部推論の言語は規定しない。外部化される判断、説明、成果物の言語だけを規定する。

## 開発原則

### 1. Durable state の責務を分離する

Coding agent の session や会話履歴を作業状態の正本にしてはならない。

作業の意味論的な状態は Issue Tracker を durable source of truth とする。
Issue Tracker には、長期的に必要な少なくとも以下の情報を残す。

- 何をしようとしているか
- なぜその作業が必要か
- 何を判断したか
- 何が完了したか
- 何が未完了か
- 人間による判断やレビュー結果

Git は implementation artifact とその履歴の durable record とする。

- source code
- configuration
- documentation
- repository に属するその他の成果物
- commit history

iro の runtime / orchestration state を durable state の代わりとして repository へ commit してはならない。
worker session は disposable とみなす。

### 2. 外部環境を勝手に補完しない

`iro` は command 実行前に、現在の `docs/behavior.md` が要求する precondition を検査する。

precondition が満たされていない場合の behavior は `docs/behavior.md` に定義する。明示的な contract なしに不足環境や外部状態を推測で補完しない。

特に、明示的な仕様なしに次のような操作を行う実装を追加しない。

- `git init`
- Git remote の追加・変更
- remote repository の作成
- credential の作成・変更
- authentication / login の自動実行
- unrelated branch / worktree の削除・変更
- ユーザー所有ファイルの上書き
- dirty worktree の破壊的な reset / clean / stash

### 3. Ownership と provenance を混同しない

破壊的な local resource 操作では、iro が安全に lifecycle を管理できる ownership evidence を要求する。
ownership が不明な local branch、worktree、file を推測で削除・上書きしてはならない。

remote PR や remote branch を誰が作成したかという provenance を、一般的な ownership evidence とみなしてはならない。
remote relation、canonical branch、local ownership mapping 等の具体的な runtime contract は `docs/behavior.md` に定義する。

### 4. Human authority と explicit operation を維持する

Human は作業対象、仕様判断、明示的な operation の実行、最終的な acceptance / merge judgment を所有する。

iro が commit、push、PR、merge、tracker mutation 等を行ってよい条件と責任境界は `docs/behavior.md` に定義する。
`AGENTS.md` はそれらの runtime operation を特定 phase の状態へ固定しない。

Issue や review feedback から、明示されていない daemon、auto-dispatch、automatic retry loop、scheduler、完全自律 lifecycle を勝手に導入しない。

runtime worker に対する Git / tracker mutation boundary は、`docs/behavior.md`、`WORKFLOW.md`、および iro が worker へ与える instructions に従う。
repository の runtime behavior を変更するコードを実装することと、coding agent 自身がその remote mutation を実行することを混同してはならない。

### 5. Scope discipline

実装中に「将来便利そう」という理由だけで機能を追加しない。

Issue の scope 外のアイデアは先回り実装せず、必要なら Issue / TODO 候補として人間へ報告する。

## 技術方針

- 実装言語は Go とする。
- CLI はグローバルにインストールして使う前提とする。
- workspace strategy は Git worktree を基本とする。
- runtime state は対象 repository へ commit しない。
- project-specific worker policy は repository root の `WORKFLOW.md` に置く。
- project-specific configuration は repository root の `iro.toml` に置く。
- 依存ライブラリは最小限にする。
- GitHub 固有処理は局所化する。
- 将来の backend / forge 拡張を妨げないが、必要になる前に過剰な plugin architecture を作らない。
- network-specific domain logic は `iro` 本体へ入れない。

現在採用している adapter、CLI、認証方式、具体的な runtime operation は `docs/behavior.md` と implementation を正とする。それらを `AGENTS.md` の恒久 policy として固定しない。

## コード品質

- 小さく明示的な実装を優先する。
- 隠れた副作用を避ける。
- command precondition を main side effect より先に評価する。
- エラーは actionable にする。
- subprocess の終了コードと stdout / stderr を適切に扱う。
- path や repository identity は必要に応じて小さな型にまとめる。
- 外部 command 呼び出しはテスト可能な境界へ寄せる。
- filesystem 操作もテスト可能な境界へ寄せる。
- test は behavior と responsibility boundary を確認するために書く。
- 過剰な abstraction は避ける。

## Git 方針

- branch 名は英語とする。
- commit message は英語とする。
- 変更はできるだけ Issue 単位に保つ。
- unrelated change を混ぜない。
- runtime における commit / push / PR / merge の responsibility は `docs/behavior.md` に定義し、`AGENTS.md` では固定しない。

## 作業完了時の報告

Coding agent は作業完了時に、日本語で簡潔に以下を報告する。

- 実施した変更
- 実行したテスト
- 成功 / 失敗
- 残っている制約または既知の問題
- 人間による次の操作が必要なら、その内容
