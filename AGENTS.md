# AGENTS.md

## 目的

このファイルは、`iro` repository で作業する Codex およびその他の coding agent に対する恒久的な project instruction である。

`iro` は Issue-driven Repository Orchestrator の作業名を持つ汎用 CLI である。Issue Tracker を永続的な作業状態の正本とし、Git worktree と Codex の実行を結び付ける。

## 文書の役割と優先関係

各文書の役割を重複させない。

- `docs/behavior.md`: runtime behavior の唯一の normative specification
- `BOOTSTRAP.md`: bootstrap MVP の初期構築指示、scope、Definition of Done
- `docs/architecture.md`: non-normative な図のみ
- `AGENTS.md`: repository 開発時の恒久的な作業原則

runtime behavior を判断するときは `docs/behavior.md` に従う。
bootstrap MVP の実装範囲を判断するときは `BOOTSTRAP.md` に従う。
`docs/architecture.md` から runtime requirement を推測してはならない。

文書間に実質的な矛盾がある場合は推測で解消せず、人間へ日本語で報告する。

## 言語ポリシー

### 人間向けの情報

次の内容は原則として日本語で記述する。

- 人間との会話
- Issue の本文、コメント、レビュー
- 設計判断の説明
- README
- `docs/` 以下の設計文書
- ADR 等の人間向け文書
- Codex が人間へ返す作業報告

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

Codex の session や会話履歴を作業状態の正本にしてはならない。

作業の意味論的な状態は Issue Tracker を正本とする。
Issue Tracker には、長期的に必要な少なくとも以下の情報を残す。

- 何をしようとしているか
- なぜその作業が必要か
- 何を判断したか
- 何が完了したか
- 何が未完了か
- 人間によるレビュー結果

Git は implementation artifact とその履歴の durable record とする。

- source code
- configuration
- documentation
- repository に属するその他の成果物
- commit history

iro の runtime / orchestration state を durable state の代わりとして repository へ commit してはならない。
Codex worker は disposable とみなす。

### 2. 外部環境を勝手に補完しない

`iro` は command 実行前に precondition を検査する。

precondition が満たされていない場合は、処理を開始せず、stderr に diagnostic と remediation hint を出す。

明示的に `docs/behavior.md` で許可されていない限り、次のような操作を自動で行わない。

- `git init`
- Git remote の追加・変更
- remote repository の作成
- `gh auth login`
- credential の作成・変更
- Codex のインストールや login
- unrelated branch / worktree の削除・変更
- ユーザー所有ファイルの上書き
- dirty worktree の reset / clean / stash

### 3. Ownership を推測しない

`iro` が作成した runtime 資源についてのみ、その lifecycle を管理してよい。

ownership が不明な資源を推測で削除・上書きしてはならない。

### 4. Human-in-the-loop

MVP では人間が Issue を選択し、`iro run <issue-number>` を実行する。

daemon、auto-dispatch、retry scheduler、完全自律実行は MVP に含めない。

review、commit、push、merge、Issue close 等を、明示的な仕様なしに自動化しない。

### 5. MVP を膨らませない

実装中に「将来便利そう」という理由だけで機能を追加しない。

MVP scope 外のアイデアは先回り実装せず、Issue または TODO 候補として報告する。

## 技術方針

- 実装言語は Go とする。
- CLI はグローバルにインストールして使う前提とする。
- GitHub 連携の MVP は `gh` CLI を利用する。
- Codex 連携の MVP はローカルの Codex CLI を利用する。
- workspace は Git worktree を使用する。
- runtime state は対象 repository へ commit しない。
- project-specific worker policy は repository root の `WORKFLOW.md` に置く。
- project-specific configuration は repository root の `iro.toml` に置く。
- 依存ライブラリは最小限にする。
- GitHub 固有処理は局所化するが、MVP で完全な plugin architecture は作らない。
- network-specific domain logic は `iro` 本体へ入れない。

## コード品質

- 小さく明示的な実装を優先する。
- 隠れた副作用を避ける。
- command precondition を実処理より先に評価する。
- エラーは actionable にする。
- subprocess の終了コードと stdout / stderr を適切に扱う。
- path や repository identity は必要に応じて小さな型にまとめる。
- 外部 command 呼び出しはテスト可能な境界へ寄せる。
- filesystem 操作もテスト可能な境界へ寄せる。
- test は behavior と responsibility boundary を確認するために書く。
- 過剰な abstraction は避ける。

## Git 方針

- branch 名は英語。
- commit message は英語。
- 変更はできるだけ Issue 単位に保つ。
- unrelated change を混ぜない。
- push / merge / PR 作成は MVP では自動で行わない。

## 作業完了時の報告

Codex は作業完了時に、日本語で簡潔に以下を報告する。

- 実施した変更
- 実行したテスト
- 成功 / 失敗
- 残っている制約または既知の問題
- 人間による次の操作が必要なら、その内容
