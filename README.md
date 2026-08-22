# iro

`iro` は Issue Tracker を作業状態の正本として扱い、Issue ごとの Git worktree で Codex worker を実行する独立した CLI です。OpenAI Symphony の設計思想に着想を得ていますが、fork、公式配布物、または OpenAI の公式実装ではありません。

## Install

Go toolchain、Git、`gh` CLI、Codex CLI を事前に用意し、GitHub と Codex の認証を済ませてください。`iro` は不足している環境を自動構築しません。

```bash
go install ./cmd/iro
```

## Bootstrap

既存の Git repository の root またはその配下で実行します。

```bash
iro init
iro doctor
iro run <issue-number>
```

`iro init` は `WORKFLOW.md` と `iro.toml` を新規作成します。`iro doctor` は環境と設定を read-only で診断します。`iro run` は configured remote の GitHub Issue を取得し、Issue 専用 worktree で fresh ephemeral Codex run を開始します。

Codex が生成した変更は commit されません。人間が worktree を review し、必要な Git 操作と Issue の lifecycle 操作を行ってください。

## Scope

bootstrap MVP は local execution と human dispatch に限定されます。daemon、scheduler、automatic commit/push/merge、Issue の自動 close、Codex session の resume は含みません。
