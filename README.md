# iro

`iro` は Issue Tracker を作業状態の正本として扱い、Issue ごとの Git worktree で Codex worker を実行する独立した CLI です。OpenAI Symphony の設計思想に着想を得ていますが、fork、公式配布物、または OpenAI の公式実装ではありません。

## Install

Go toolchain と Git を事前に用意してください。`iro run` または `iro review` を利用する場合は、さらに `gh` CLI、Codex CLI、および各認証が必要です。`iro` は不足している環境を自動構築しません。

```bash
go install ./cmd/iro
```

## Bootstrap

既存の Git repository の root またはその配下で実行します。

```bash
iro init
iro doctor
iro status
iro run <issue-number>
iro review <pr-number>
iro cleanup <issue-number>
```

`iro init` は `WORKFLOW.md` と `iro.toml` を新規作成します。`iro doctor` は環境と設定を read-only で診断します。`iro status` は ownership mapping に対応するローカル Issue workspace の機械状態を read-only で表示します。`iro run` は configured remote の GitHub Issue を取得し、Issue 専用 worktree で fresh ephemeral Codex run を開始します。`iro review` は default branch を base とし、exactly 1 件の origin Issue closing relation を持つ open PR（Draft を含む）を disposable workspace の fresh Reviewer で独立評価し、最終報告をそのまま PR comment に投稿します。
`iro cleanup` は Human が明示した Issue について、ownership を検証できる clean な local worktree と local branch を安全に削除し、最後に ownership mapping を削除します。GitHub Issue / PR の状態や remote branch は確認・変更しません。

`iro run` は configured repository の default branch の clean な checkout から実行します。worker 成功後、iro が変更を commit / push し、default branch を base とする通常の open PR を作成します。既存の関連 PR がある場合は停止します。PR number と `iro land <pr-number>` を表示し、同じヒントを PR comment に投稿します（`land` command 自体は未実装です）。`iro review` は current checkout の branch、dirty state、target PR の local branch/worktree、ownership mapping を要求しません。AI Review は advisory information であり、Human が merge を判断します。

`iro review` は advisory review であり、prompt-isolation の security boundary ではありません。Reviewer は PR HEAD 上で動作するため、PR が `AGENTS.md` などの agent instruction file を変更する場合、その変更が Reviewer の判断に影響する可能性があります。repository / agent policy 自体を変更する PR は、必要に応じて Human または独立 session で追加レビューしてください。

`iro init` が生成するファイルの commit / push は引き続き人間の責任です。

## Scope

実行は human dispatch に限定されます。daemon、scheduler、自動 merge、Issue の直接 close、`iro run` による Draft PR 作成 option、Codex session の resume は含みません。Author worker は file modification と validation だけを担当し、Git / tracker lifecycle mutation は iro が担当します。
