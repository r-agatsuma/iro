# Operator cookbook

> Runtime requirements are defined by `docs/behavior.md`. This cookbook provides human-operated recovery and troubleshooting recipes for the current behavior.

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

まず initialized repository から確認します。

```bash
iro status
git worktree list --porcelain
```

`iro status` は local ownership mapping に対応する workspace の状態を表示します。`DIRTY` は変更の存在、`BROKEN` は対応関係の不整合です。PR の有無や作業の完了を意味しません。mapping がなければ行も表示されないため、失敗時の出力・local log にある worktree path も確認します。

表示された対象 path を指定し、Human が直接 inspect します。以下の `/path/to/issue-worktree` は実際の path に置き換えてください。

```bash
git -C /path/to/issue-worktree status --short --untracked-files=all
git -C /path/to/issue-worktree diff
git -C /path/to/issue-worktree diff --cached
git -C /path/to/issue-worktree ls-files --others --exclude-standard
git -C /path/to/issue-worktree log -5 --oneline
```

保存するなら次節の方法を選び、保存結果を確認してから clean state に戻します。破棄するなら「changes を破棄する」を参照してください。`BROKEN` の場合は dirty の解消だけで直るとは限りません。mapping / branch / worktree の不整合を調査し、所有が不明な resource を削除しないでください。

clean state と remote delivery の状況を確認した後、元の repository の clean な default branch checkout から明示的に再実行します。

```bash
iro status
iro run 123
```

`123` は対象 Issue number です。これは session resume ではなく fresh Author の rerun です。整合した既存 owned worktree は再利用され、その branch が default branch の新しい HEAD に自動追従することはありません。active delivery PR がある場合は Run を繰り返さず、relation を確認して `iro revise <pr-number>` を使います。

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

delivery PR がまだ存在しない場合、`iro revise <pr-number>` の対象はありません。partial changes を保存または破棄して Run の precondition を満たした後に、`iro run <issue-number>` を fresh rerun します。既存 remote branch との衝突や relation の不整合が残る場合は、その診断に従って Human が整理します。iro は自動 rollback しません。

## cleanup が dirty state で reject された

`iro cleanup <issue-number>` は automatic discard を行いません。上の inspect と保存・破棄を行い、対象 worktree が clean になった後、対象自身とは別の checkout から再実行します。

```bash
iro status
iro cleanup 123
```

clean だけでは十分ではありません。ownership が整合し、Issue branch tip が実行元 checkout の HEAD の履歴に含まれている必要があります。含まれない場合は、その履歴を含む integration checkout から再実行します。保存用 branch を作っただけではこの ancestor 条件は満たしません。`cleanup --force` はありません。cleanup は remote PR / branch を確認も変更もしないため、remote merge 済みという推測では local 履歴の条件を回避できません。

## remote PR はあるが local state がない

現在の `iro revise <pr-number>` は、次の relation を検証でき、canonical local mapping / branch / worktree がすべて欠落していれば、remote PR HEAD から materialize できます。

- open PR の head が configured repository の `iro/issue-N`
- base が configured repository の default branch
- GitHub native closing Issues が同 repository の exactly `{N}`
- その Issue / canonical branch の active delivery PR が対象だけ

initialized repository から実行します。

```bash
iro doctor
iro revise 456
```

`456` は PR number です。creator が Human でも relation を満たせば対象です。Revise は fresh Author を実行し、成功すると同じ branch に commit / push して PR を更新します。

一部だけ欠けた partial state、dirty state、remote HEAD と異なる divergent state は自動修復しません。全欠落に見せるために mapping だけを削除せず、保存と所有関係を確認して診断を解消してください。

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
