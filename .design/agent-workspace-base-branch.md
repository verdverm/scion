# Agent Workspace Source Branch Configuration

## Purpose

Allow specifying which remote branch/tag/commit gets cloned as the source content for agent workspaces, independent of the local target branch name.

## The Problem

When you switched your repo's default on GitHub from `main` to `opencode-support`, agents still cloned `main`. The Hub stored `main` as a project label at creation time and used it as a stale default, never probing the remote for the current HEAD.

Additionally, in local mode, `git worktree add -b <slug> <path>` creates from HEAD — there was no way to specify a different source (e.g., `origin/opencode-support`) without checking it out locally first.

Verified: `git ls-remote --symref https://github.com/verdverm/scion HEAD` returns `opencode-support`.

## Source vs Target Branch — Clarification

- **Source branch** — which remote branch/tag/commit gets cloned/fetched as the content
- **Target branch** — the name of the local worktree branch (slugified agent name, intentional)

The `--branch` flag controls the **target** branch name. The **source** branch is what was broken.

## New Flag: `--source`

```
scion start <agent> --source origin/opencode-support
scion create <agent> --source v1.0.0
```

`--source` specifies the git ref to use as the source for the workspace content. It can be any valid git commit-ish: a branch name (`origin/main`), a tag (`v1.0.0`), or a commit SHA. When omitted, the system uses the repo's default branch (detected dynamically).

## How Branch Resolution Works

### Local Mode (worktree-based)

```
--source flag → ProvisionAgent(source) → CreateWorktree(path, targetBranch, source)
```

- `cmd/start.go:52` and `cmd/create.go:280` register the `--source` flag
- `cmd/common.go:434` resolves effective source: CLI flag → inline config → repo default branch
- `pkg/agent/provision.go:448-458` resolves source branch and passes to `CreateWorktree` (new agent path)
- `pkg/agent/provision.go:1117-1128` resolves source/target branch and recreates missing worktree (`GetAgent` path)
- `pkg/util/git.go:195-257` creates the worktree: `git worktree add --relative-paths -b <target> <path> [<source>]`

When no `--source` is specified, it uses `util.DefaultBranch(projectDir)` which probes the repo's HEAD via `git symbolic-ref --short HEAD`.

### Target Branch vs Source Branch — Both Paths

There are two worktree creation paths, both must resolve source and target correctly:

1. **`ProvisionAgent` (new agent)** — `pkg/agent/provision.go:448-458`
   - Target branch: `branch` flag → `api.Slugify(agentName)` fallback
   - Source branch: `source` flag → `util.DefaultBranch(projectDir)` fallback

2. **`GetAgent` worktree recreation (existing agent, missing workspace)** — `pkg/agent/provision.go:1117-1128`
   - Target branch: `branch` flag → `api.Slugify(agentName)` fallback
   - Source branch: `source` flag → `util.DefaultBranch(projectDir)` fallback

**Critical:** Both paths must use `api.Slugify(agentName)` for the target branch fallback. Using `util.DefaultBranch(projectDir)` as the target branch fallback would cause multiple agents to collide on the same branch, breaking isolation.

### Hub Mode (git clone inside container)

```
Hub CreateAgent → populateAgentConfig() → GitCloneConfig.Branch
  → buildStartContext() → SCION_GIT_BRANCH env var
    → sciontool gitCloneWorkspace() → branch
```

The Hub now probes the git remote dynamically via `git ls-remote --symref <url> HEAD` to get the current default branch. The stored label (`scion.dev/default-branch`) is only a fallback when the remote is unreachable.

## New File: `pkg/hub/default_branch.go`

Added `resolveDefaultBranch(cloneURL, storedBranch string)` that **probes the git remote first** via `git ls-remote --symref <url> HEAD` to get the current default branch. The stored label is only a fallback when the remote is unreachable.

```go
func (s *Server) resolveDefaultBranch(cloneURL, storedBranch string) string {
    if cloneURL != "" {
        if detected := s.detectDefaultBranch(cloneURL); detected != "" {
            return detected
        }
    }
    if storedBranch != "" {
        return storedBranch
    }
    return "main"
}
```

## Updated: `pkg/hub/handlers.go`

Replaced all three hardcoded `"main"` fallbacks with calls to `resolveDefaultBranch`:

- Line 8567: `defaultBranch := s.resolveDefaultBranch(cloneURL, project.Labels["scion.dev/default-branch"])`
- Line 8587: `defaultBranch := s.resolveDefaultBranch(cloneURL, project.Labels["scion.dev/default-branch"])`
- Line 3797: `defaultBranch := s.resolveDefaultBranch(cloneURL, project.Labels["scion.dev/default-branch"])`

## Updated: `pkg/util/git.go`

`CreateWorktree(path, branch, source string)` now accepts an optional source argument:

- `git worktree add --relative-paths -b <branch> <path>` (no source — uses HEAD)
- `git worktree add --relative-paths -b <branch> <path> <source>` (uses specified commit-ish)

## Updated: `pkg/util/git.go`

`DefaultBranch(dir string)` probes the local repo's default branch:

1. `git symbolic-ref --short refs/remotes/origin/HEAD` — remote HEAD reference
2. Falls back to `git symbolic-ref --short HEAD` — current HEAD branch
3. Falls back to `"main"`

## Updated: `pkg/api/types.go`

- `StartOptions.Source string` — source branch for workspace content
- `ScionConfig.Source string` — inline config support for source branch

## Updated: `pkg/store/models.go`

- `AgentAppliedConfig.Source string` — persisted source branch for Hub agents

## Updated: `pkg/hubclient/agents.go`

- `CreateAgentRequest.Source string` — source branch in Hub API requests

## Updated: `pkg/runtimebroker/types.go`

- `CreateAgentConfig.Source string` — source branch passed to brokers

## Updated: `pkg/runtimebroker/start_context.go`

- Wires `opts.Source = in.Config.Source` through the broker dispatch flow

## CLI Flag Registration

- `cmd/start.go:52`: `--source` flag for `scion start`
- `cmd/create.go:280`: `--source` flag for `scion create`
- `cmd/common.go:67`: package-level `source` variable
- `cmd/common.go:434`: inline config override resolution

## Build Verification

```
$ go build ./...
# passes
```

## Remaining: Test Files

Test files need `source` parameter added to `ProvisionAgent` and `GetAgent` calls:
- `pkg/agent/provision_test.go` — ~35 calls
- `pkg/agent/provision_reload_test.go` — 2 calls
- `pkg/agent/opencode_provision_test.go` — 1 call
- `pkg/agent/provision_compose_test.go` — 20 calls
- `pkg/agent/provision_home_test.go` — 2 calls
- `pkg/agent/delete_test.go` — 2 calls (already fixed via sed)
- `pkg/util/git_test.go` — 11 calls (already fixed via sed)

All test files have `ProvisionAgent(ctx, name, tpl, "", "", proj, "", "", "", "")` — need to add `""` for source between `optionalStatus` and `workspace`, making it `ProvisionAgent(ctx, name, tpl, "", "", proj, "", "", "", "", "")`.

## Testing Plan

1. **Local mode (default):** Run `scion start my-agent` without `--source`. Verify the worktree is created from HEAD (e.g., `opencode-support`).

2. **Local mode (explicit source):** Run `scion start my-agent --source origin/main`. Verify the worktree target branch is the slugified agent name, but its content comes from `origin/main`.

3. **Hub git-clone mode (branch rename):** Create an agent on a project whose default branch was renamed. Verify it clones the new default branch, not the stale label value.

4. **Hub git-clone mode (agent override):** Create an agent with `--source feature/foo`. Verify it clones `feature/foo` instead of the project default.

5. **Hub shared-workspace mode:** Create an agent without `--source` on a shared-workspace project. Verify it uses the project's current default branch.

6. **Remote unreachable:** If the git remote is unreachable, fall back to stored label, then to `"main"`.

7. **Inline config:** Specify source in `--config` YAML/JSON.

## Edge Cases

- **Source branch doesn't exist on remote:** `sciontool` already handles this — it tries the agent branch, then falls back to the default branch.
- **Remote unreachable:** Falls back to stored label, then to `"main"`.
- **Private repos:** `git ls-remote` may fail without auth; falls back to stored label.
- **Both agent and project specify branch:** Agent-level `--source` takes precedence.
- **Source is a commit SHA:** Works — git worktree accepts any commit-ish.
- **Source is a tag:** Works — git worktree accepts any commit-ish.

## All Branch-Related Files (Reference)

| File | Lines | Purpose |
|------|-------|---------|
| `cmd/start.go` | 52 | `--source` flag for `scion start` |
| `cmd/create.go` | 280 | `--source` flag for `scion create` |
| `cmd/common.go` | 67, 434 | Package-level `source` var; effective source resolution |
| `cmd/hub.go` | 262, 284-285, 341, 351, 1493-1501, 1586, 1619-1642 | `hub projects create --branch`; detect/parse default branch from git remote; store as label |
| `pkg/api/types.go` | 452, 697, 771 | `ScionConfig.Source`, `GitCloneConfig.Branch`, `StartOptions.Source` |
| `pkg/config/templates.go` | 729-731 | Merge `ScionConfig.Branch` over template config |
| `pkg/config/init.go` | 238-277 | `InitProject()` — branch is NOT stored here |
| `pkg/agent/provision.go` | 399-403, 448-458, 1117-1128 | Source/target branch resolution and worktree creation (ProvisionAgent + GetAgent recreation) |
| `pkg/agent/run.go` | 142 | Source passed from `StartOptions` to `GetAgent()` |
| `pkg/hubclient/agents.go` | 168 | `CreateAgentRequest.Source` field |
| `pkg/hub/handlers.go` | 200, 3797, 8528, 8567, 8587 | Hub git clone branch resolution (now uses `resolveDefaultBranch`) |
| `pkg/hub/default_branch.go` | new | `resolveDefaultBranch()` — probes git remote dynamically |
| `pkg/runtimebroker/types.go` | 386 | `CreateAgentConfig.Source` field |
| `pkg/runtimebroker/start_context.go` | 362 | Wires `opts.Source` through broker dispatch |
| `pkg/store/models.go` | 138 | `AgentAppliedConfig.Source` field |
| `cmd/sciontool/commands/init.go` | 1205-1207 | Agent-side `SCION_GIT_BRANCH` env var, default "main" |
| `cmd/delete.go` | 63-64, 130, 227, 336, 349-353 | `--preserve-branch` flag for `scion delete` |
| `pkg/util/git.go` | 165-192, 195-257 | `DefaultBranch()`, `CreateWorktree()` with source parameter |
