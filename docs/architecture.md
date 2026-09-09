# iro Architecture

> 現在 architecture の non-normative diagrams。runtime requirement の正本は `docs/behavior.md` だけである。

## Authority and durable state

```mermaid
flowchart TB
    H["Human<br/>repository authority<br/>specification / target selection / final judgment"]
    ISSUE["GitHub Issue<br/>WHAT / WHY authority<br/>durable semantic state"]
    PR["Pull Request<br/>implementation / review surface<br/>durable discussion"]
    GIT["Git<br/>durable artifacts + history"]
    IRO["iro<br/>explicit operation executor"]
    AUTHOR["disposable Author worker"]
    REVIEWER["disposable independent Reviewer worker"]
    BOUNDARY["worker boundary<br/>no Git metadata / history / remote<br/>no tracker mutation"]

    H -->|"specification / decisions"| ISSUE
    ISSUE -->|"current WHAT / WHY"| H
    H -->|"Run / Review / Revise / Land / Cleanup"| IRO
    H -->|"direct commit / push is allowed"| GIT
    H -->|"direct PR / review / merge judgment"| PR
    ISSUE -->|"task WHAT / WHY"| IRO
    IRO -->|"launch fresh session"| AUTHOR
    IRO -->|"launch fresh session"| REVIEWER
    AUTHOR -->|"uncommitted file changes + report"| IRO
    REVIEWER -->|"opaque final response"| IRO
    AUTHOR -.->|"must not cross"| BOUNDARY
    REVIEWER -.->|"must not cross"| BOUNDARY
    IRO -->|"Issue read / result comment"| ISSUE
    IRO -->|"PR create / comment / merge"| PR
    IRO -->|"branch / worktree / commit / push"| GIT
```

## Remote delivery lifecycle

```mermaid
flowchart LR
    ISSUE["Executable Issue"] --> RUN["Run"]
    RUN --> PR["normal open PR"]
    PR --> HUMAN["Human judgment"]
    PR -.-> REVIEW["optional Review"]
    REVIEW -->|"opaque PR comment"| PR
    PR -.-> REVISE["optional Revise"]
    REVISE -->|"same branch / same PR"| PR
    HUMAN -->|"iro land invocation<br/>is merge authorization"| LAND["Land"]
    LAND --> MERGE["normal merge commit"]
    MERGE --> CLOSE["GitHub native<br/>Issue close"]
    MERGE -.->|"separate operation, later if needed"| CLEANUP["Cleanup"]
```

## Canonical delivery relation

```mermaid
flowchart LR
    subgraph REPO["configured repository"]
        D["default branch D<br/>canonical integration base"]
        B["iro/issue-N<br/>canonical delivery branch"]
        PR["normal open PR<br/>creator identity is not eligibility"]
        ISSUE["Issue #N<br/>WHAT / WHY"]

        D -->|"Run from clean named<br/>default-branch checkout"| B
        B -->|"head"| PR
        D -->|"base"| PR
        PR ---|"GitHub native closing relation<br/>exactly Issue #N"| ISSUE
        PR -->|"Land merge"| D
    end

    HINT["delivery hint comment<br/>Human UX only"] -.-> PR
    CHECK["eligibility<br/>Issue / canonical branch / PR relation<br/>not PR provenance"] -.-> PR
```

## Local workspace responsibilities

```mermaid
flowchart TB
    subgraph RUNPATH["Run"]
        RUN["Run"] --> MAP["local runtime<br/>ownership mapping"]
        RUN --> CWS["local runtime<br/>canonical Issue worktree<br/>iro/issue-N"]
        RUN --> AW["fresh disposable Author"]
        AW -->|"files / tests / report"| CWS
        CWS -->|"iro commits and delivers"| REMOTEPR["remote delivery PR"]
    end

    subgraph REVIEWPATH["Review"]
        REVIEW["Review<br/>no target local state required"] --> TMP["temporary clone<br/>detached verified PR HEAD"]
        TMP --> RW["fresh independent Reviewer"]
        RW -->|"final response"| COMMENT["PR comment"]
        RW -->|"after process"| REMOVE["remove disposable workspace"]
    end

    subgraph REVISEPATH["Revise"]
        REVISE["Revise"] --> LOCAL{"canonical local state"}
        LOCAL -->|"all absent"| MATERIALIZE["materialize from<br/>validated remote PR HEAD"]
        LOCAL -->|"consistent + clean<br/>same HEAD"| REUSE["reuse canonical worktree"]
        LOCAL -->|"partial / dirty / divergent"| STOP["stop; no automatic repair"]
        MATERIALIZE --> MAP
        MATERIALIZE --> CWS
        REUSE --> CWS
    end

    subgraph FINISHPATH["Remote completion and local teardown"]
        LAND["Land<br/>remote-only relative to target Issue state"] --> REMOTEPR
        CLEANUP["Cleanup<br/>local-only"] --> VERIFY["verify ownership + clean state"]
        VERIFY --> ORDER["remove worktree<br/>then safe-delete branch<br/>then remove mapping last"]
    end

    LAND -.->|"does not run"| CLEANUP
```

## Review snapshot provenance and opaque forwarding

```mermaid
flowchart LR
    META["PR preflight metadata"] --> BASE["observed base branch D<br/>observed base OID B"]
    META --> HEAD["observed PR HEAD OID H"]
    HEAD --> VERIFY["disposable workspace<br/>verify workspace HEAD == H"]
    MODEL["resolved model identity<br/>if unavailable: explicit unknown<br/>never inferred"] --> TRUST["trusted provenance<br/>supplied by iro"]
    BASE --> TRUST
    VERIFY -->|"verified Reviewed HEAD OID H"| TRUST
    TRUST --> REVIEWER["Reviewer"]
    REVIEWER -->|"final response as opaque bytes"| IRO["iro<br/>no parse / normalize / reconstruct<br/>no prepend / append"]
    IRO -->|"unchanged body"| COMMENT["PR conversation comment"]
    TRUST -.->|"snapshot identification only<br/>not freshness or Land gate"| HUMAN["Human"]
```

## HEAD-bound Land on the configured host

```mermaid
flowchart LR
    HUMAN["Human<br/>explicit iro land PR"] --> VALIDATE["validate selected PR"]
    REL["open non-Draft PR<br/>iro/issue-N to default branch D<br/>closing Issues exactly N<br/>creator ignored"] --> VALIDATE
    POLICY["repository merge policy<br/>BEHIND alone is allowed"] --> VALIDATE
    HOST["configured remote host<br/>github.com only<br/>reject GH_HOST / GH_REPO mismatch"] --> VALIDATE
    VALIDATE -->|"validated PR HEAD OID H"| API["github.com merge API<br/>normal merge, sha = H"]
    API --> GITHUB{"GitHub final policy enforcement"}
    GITHUB -->|"accepted"| MERGED["merge / native Issue close"]
    GITHUB -->|"up-to-date policy rejection<br/>or HEAD drift"| FAILURE["failure<br/>no bypass / branch update / retry<br/>no merge-method fallback"]
    API -.->|"no target Issue worktree required<br/>no local cleanup"| LOCAL["local Issue resources unchanged"]
```

## Instruction and policy layers

```mermaid
flowchart TB
    DI["iro developer instructions<br/>operation control + safety boundary"]
    AG["AGENTS.md<br/>repository development policy"]
    WF["WORKFLOW.md<br/>repository worker policy"]
    TASK["Issue / PR payload<br/>task and review input, not policy"]
    WORKER["Author or Reviewer worker"]

    DI --> WORKER
    AG --> WORKER
    WF --> WORKER
    TASK --> WORKER
    DI -.->|"requires reading"| WF
```
