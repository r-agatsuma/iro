# WORKFLOW.md

## Workload / target

この repository の workload は Go 製 iro CLI の implementation、test、configuration、documentation の開発である。
Issue の目的と acceptance criteria を確認し、operation workspace で作業する。
恒久的な project policy は Codex 標準機構が読み込む `AGENTS.md` に従う。

## Allowed operations

- Author は Issue scope 内の source、test、configuration、documentation を編集してよい。
- local build、test、static analysis、および read-only Git inspection を行ってよい。
- dependency resolution、local test に必要な通信、read-only な情報取得を行ってよい。
- Reviewer は read-only inspection / validation に限る。disposable build / test artifact は許容する。

## Prohibited operations

- unrelated change や scope 外の追加実装を行わない。
- worker は iro core が所有する Git metadata / index / refs / history / delivery remote や GitHub Issue / PR lifecycle を変更しない。
- external service の設定変更、deployment 等の external workload mutation は、この repository の worker workload として許可しない。
- Reviewer は source の修正や external workload mutation を行わない。

## Access / tool usage

- repository 内の source と文書を参照し、Go toolchain と local shell で実装・検証する。
- Git command は read-only inspection に限定する。Git / tracker の lifecycle operation は iro orchestration が担当する。
- task data は iro が渡す Issue / PR context を使用し、worker が `gh` で直接取得・変更しない。

## Credential references

この workload では worker に external mutation 用の credential を要求しない。
dependency resolution 等で認証が必要な場合は、実行環境に既存の認証設定を使用する。
secret value を repository、report、log に保存・出力しない。

## Validation

- 変更に関係する Go test を実行する。影響が共通部分に及ぶ場合は `go test ./...` を実行する。
- Go 実装の変更に関連する場合は `go vet ./...` を実行する。
- `git diff --check` で差分の whitespace error がないことを確認する。

## Reporting requirements

作業結果を日本語で簡潔に報告する。実施した変更、実行した検証と成功 / 失敗、correctness または acceptance criteria に影響する残存制約を記載する。
Author report では、iro が後続で管理する commit / push / PR の状態を列挙しない。
未実施の任意検証は列挙せず、acceptance criteria または具体的な correctness risk が未解決になる場合だけ記載する。
人間の判断や操作が必要な場合は、その内容を明示する。
