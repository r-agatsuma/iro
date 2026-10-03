# Operator cookbook

> 現行の #83 foundation baseline に対する Human 向け手順集。runtime requirement の正本は `docs/behavior.md` である。

この文書は non-normative な Human 向けの手順集です。runtime requirement の正本は [behavior.md](behavior.md) です。以下の Git 操作は Human が対象と保存範囲を確認して選択するもので、worker の Git mutation 権限を拡張しません。iro は automatic repair、reset / stash / clean、自動 retry を行いません。

## operation ごとに Codex model を指定する

`run`、`review`、`revise` では番号の後ろに `--model <model>` または `-m <model>` を指定できます。省略時は Codex の configuration / default selection に委譲され、iro.toml に model default はありません。

```bash
iro run 123 --model <model>
iro review 456 -m <model>
iro revise 456 --model <model>
```

指定した requested model は Review report の resolved model identity を表しません。現在の runtime が resolved identity を trusted metadata として公開しないため、Review provenance の Model は unknown のままです。

## Run failure で dirty worktree が残った

まず対象の local Git repository から確認します。

```bash
iro status
git worktree list --porcelain
```

`iro status` は local `iro/*` refs と attached registered worktrees、および登録済み runtime workspace の branch / path / HEAD を表示します。project files や v1 ownership JSON は参照せず、dirty state、PR の有無、作業の完了を判定しません。Status は削除の安全性を保証しないため、必要な内容は Human が直接 inspect します。

表示された対象 path を指定し、Human が直接 inspect します。以下の `/path/to/issue-worktree` は実際の path に置き換えてください。

```bash
git -C /path/to/issue-worktree status --short --untracked-files=all
git -C /path/to/issue-worktree diff
git -C /path/to/issue-worktree diff --cached
git -C /path/to/issue-worktree ls-files --others --exclude-standard
git -C /path/to/issue-worktree log -5 --oneline
```

保存するなら次節の方法を選び、保存結果を確認します。破棄するなら「changes を破棄する」を参照してください。branch / worktree registration / HEAD の不整合は cleanliness とは別に調査します。

remote delivery の状況を確認した後、元の repository の clean な named branch checkout から明示的に再実行できます。その local HEAD が configured remote の同名 branch tip と一致する必要があります。default branch は必須ではありません。

```bash
iro status
iro run 123
```

`123` は対象 Issue number です。後続 Run は新しい delivery ID / branch / workspace の fresh Author です。以前の dirty / failed workspace は再利用せず、その保存や Cleanup は新しい Run の precondition ではありません。既存 PR を更新したい場合は、対象を確認して `iro revise <pr-number>` を選びます。同じ Issue の複数 delivery は許容されます。

## 中断した managed Run 後に partial state が残った

Ctrl-C や worker の中断後には、次の resource が部分的に残る可能性があります。失敗診断の delivery ID / exact path / ref を確認します。

- local branch `iro/issue-123-D`
- Git の worktree registration
- filesystem worktree path `.../issue-123-D`
- local Author report / delivery outcome log

まず、失敗診断に表示された path を使って個別に確認します。`iro status` は read-only ですが、`iro cleanup 123` は未保存変更も破棄する操作です。必要な保存を確認してから対象を選び、別の Issue まで処理する bulk `iro cleanup` を状態確認だけの目的で呼び出さないでください。

```bash
iro status
git branch --list 'iro/issue-123' 'iro/issue-123-*'
git worktree list --porcelain
test -e /reported/issue-worktree-path && echo "worktree path exists"
```

worktree が残っている場合は変更を inspect し、必要な保存を確認してから対象 Issue を指定して `iro cleanup 123` を実行します。cleanup は dirty / untracked / ignored files と unpublished commits を保護せず、Git force removal / force branch deletion を試行します。

linked worktree directory の手動削除だけでは Git registration が残る場合があります。Status は branch-only / missing-path registration も表示し、Cleanup は発見済み exact target に best-effort removal を試行します。登録も ref も失った filesystem-only residue は自動発見を保証しないため、失敗診断の既知 path を Human が確認します。iro は未知の path を scan せず、自動 repair / adopt / prune を行いません。

v1 ownership JSON は Run / Review / Revise / Land / Status / Cleanup の authority ではありません。JSON の削除・migration によって precondition を解消する手順はありません。後続 Run は新しい delivery を作り、Revise は選択 PR の exact head ref に対応する現在の local state を検証します。Cleanup は runtime log や旧 JSON を削除しません。

## partial changes を保存したい

保存先は対象 worktree の外を選びます。単一の `git diff > file.patch` は万能 backup ではありません。

| 方法 | 保存範囲と注意点 |
|---|---|
| `git diff --binary > /outside/unstaged.patch` | tracked の unstaged 差分。staged / untracked / ignored は含みません |
| `git diff --cached --binary > /outside/staged.patch` | staged 差分。上の patch と別に保存します |
| 必要な file / directory の外部コピー | untracked / ignored を含め、必要なものを明示して保存できます。Git の index 状態は別途記録します |
| `git stash push -u -m "Preserve partial iro work"` | tracked と untracked を保存し worktree から退避します。ignored は含まず、stash は local repository 内にあります |
| Human による commit | 選択して stage した内容を履歴へ保存します。untracked は明示的に add したものだけが対象です |

Git command は対象 worktree 内、または `git -C /path/to/issue-worktree ...` で実行します。patch の保存だけでは worktree は clean になりません。patch は適用元の HEAD OID も記録し、コピーした untracked / ignored file と合わせて復元できることを確認します。staged と unstaged の区別を復元する場合は staged patch を index に適用してから unstaged patch を適用するなど、保存方法に合う手順を選びます。

stash は `git stash list` と `git stash show --stat --include-untracked 'stash@{0}'` で対象を確認します。復元は必要な時に `git stash apply --index 'stash@{0}'` 等を Human が選びます。conflict があり得るため、復元確認前に stash を削除しないでください。ignored に必要な成果物があるなら別途外部コピーします。

commit で保存する場合は内容を確認して必要な path だけを add / commit し、cleanup 後も保持する必要があれば別の保存用 branch や外部 backup でも参照を確保します。local commit により remote PR HEAD と divergent になると `iro revise` は拒否します。保存だけで Revise の precondition が満たされるとは限りません。

## changes を破棄する

対象の差分を読み、必要な保存が済んだ場合にだけ Human が実行します。次は tracked の staged / unstaged 変更を HEAD の内容へ戻す例で、未保存内容は失われます。

```bash
git -C /path/to/issue-worktree restore --source=HEAD --staged --worktree -- .
```

untracked は別です。まず削除予定を preview します。

```bash
git -C /path/to/issue-worktree clean -nd
```

preview の対象を確認してから、不要と判断した path だけを指定します。

```bash
git -C /path/to/issue-worktree clean -fd -- path/to/disposable-file
git -C /path/to/issue-worktree status --short --untracked-files=all
```

上記は既存 commit を取り消す操作ではありません。ignored file はこの clean の対象外です。iro の cleanup では ignored state が worktree とともに削除され得るため、必要なものは外部へ保存してください。

## PR 作成前に Run が failure した

Author failure と delivery failure を失敗出力・local log で区別します。delivery failure では commit / push が完了している場合もあるため、GitHub の PR 一覧と branch、local HEAD を Human が確認します。通信失敗だけを根拠に PR がないと判断しません。

delivery PR がまだ存在しない場合、`iro revise <pr-number>` の対象はありません。Run の source checkout precondition を満たした後に、`iro run <issue-number>` で別 delivery を作れます。以前の local changes / commit / remote branch は独立したままです。結果不明の PR create を再試行したり、既存 PR を自動探索して採用したりしません。

push が不明なら remote branch が更新済み、PR-create response が不明なら PR が作成済みの可能性があります。report comment の失敗だけなら confirmed PR delivery は成功です。診断と outcome log はこの区別を保持します。明示 Cleanup は local purge だけであり、不明な remote outcome を確定・修復する手段ではありません。

## destructive cleanup が一部失敗した

`iro cleanup <issue-number>` は dirty state や ancestor 関係で拒否しません。Git / filesystem mechanism の失敗でも独立した削除は進むため、non-zero の後も以前と同じ状態とは限りません。diagnostic の branch / exact path を確認します。

```bash
iro status
iro cleanup 123
```

remaining / unknown registration や path は confirmed success ではありません。iro は automatic unlock / prune / repair を行わず、未知の filesystem path を scan しません。復元や残骸の手動処理は現在の local state と保存範囲を確認した Human が判断します。`cleanup --force` はなく、通常の invocation 自体が destructive purge です。remote PR / branch は確認も変更もしません。

## remote PR はあるが local state がない

現在の managed `iro revise <pr-number>` は、次の条件を満たせば exact remote PR HEAD `H1` から materialize できます。

- configured repository の readable open PR で、head repository が同じ repository
- current raw body 内の local token が、同 repository の readable Issue ちょうど1件へ解決できる
- remote の exact head ref tip が H1 と一致し、push destination と required context が検証可能
- H1 に regular な `WORKFLOW.md` があり、固定 starting policy として読める

initialized repository から実行します。

```bash
iro doctor
iro revise 456
```

`456` は PR number です。creator が Human / unmanaged producer でも自身の条件だけで判断します。default base、canonical naming、native closing relation、active PR / shared-head の一意性は要求しません。`iro/*` head は exact ref の clean / H1 に一致する registered worktree があれば再利用し、なければ新規作成します。Human branch は fresh detached workspace で扱い、既存 Human checkout / local branch を採用しません。

selected iro worktree の dirty / divergent / conflicting state は自動修復しません。local commit は H1 の child として作り、push 直前に一度だけ target を再検証します。remote drift / deletion / closed / unreadable / destination change では local commit を保持して push しません。body / base の編集は実行中の binding を変えず、次の invocation の managed specification はその時の body から解決します。Cleanup の Issue number は physical branch / workspace namespace のままです。

## Land 後に default branch を同期する

`iro land <pr-number>` が成功すると、次の `iro run` 前に local default branch を remote と同期するための informational hint が表示されます。Land は remote merge だけを行い、local checkout を変更しません。

同期方法の例は次のとおりです。対象の local checkout と実行 location は Human が選びます。この hint は Run の source branch を default branch に限定せず、実際に選ぶ named branch の local HEAD と remote の同名 tip を一致させます。

```text
Sync your local default branch with the remote before the next iro run.
For example: git pull
```

iro 自身は同期 command を実行せず、local branch が同期済みかも検証しません。merge が失敗した場合、この success-only hint は表示されません。

## 古い iro binary を実行している疑い

source を更新しても installed binary は自動更新されません。

```bash
command -v iro
iro version
go env GOBIN
go env GOPATH
```

`iro version` は project 外でも使えます。revision / vcs-time / modified / Go version は build に記録された情報です。`unknown` は取得不能を意味し、現在の source revision と同じという意味ではありません。`go install` の方法や build 条件によって VCS 情報が欠けることがあります。

必要なら、意図した source checkout の root で再 install します。

```bash
go install ./cmd/iro
command -v iro
iro version
```

`GOBIN` が空なら通常の install 先は `$(go env GOPATH)/bin` です。その directory を PATH から参照できるよう Human が設定します。別 directory の古い binary が先に選ばれていないかも確認します。shell が executable location を記憶している場合は、shell の command cache を更新するか新しい shell で確認します。iro が shell dotfile を編集することはありません。

initialized project では `iro doctor` が実行中の iro と PATH 上の git / gh / codex の path・version、既存の認証・project precondition をまとめて表示します。補足 metadata が `unknown` でも、それだけでは health failure になりません。

`FAIL: GitHub CLI context` は configured repository と `GH_HOST` / `GH_REPO` の不整合を示します。表示された configured value、observed value、remediation を確認し、Human が該当する変数を unset するか configured value に設定してから再実行します。例えば configured host が `github.com` なら `unset GH_HOST` または `export GH_HOST=github.com` で修正できます。context が不明・不整合な間は GitHub 認証確認を実行せず、他の診断を続けます。doctor 自身は環境や認証を変更しません。
