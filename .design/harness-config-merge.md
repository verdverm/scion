# Harness Config Merge Process

## Overview

Scion composes agent configuration from multiple layers: settings, profiles, harness-configs (both on-disk and template-level), inline config, and the harness implementation itself. Each layer contributes fields that flow into the container environment, `scion-agent.json`, and harness-native config files.

This doc tracks every field, every merge point, and which layers actually propagate each field.

## Configuration Sources (in resolution order)

1. **Settings harness_configs** — `~/.scion/settings.yaml` → `harness_configs.<name>`
2. **Profile env/volumes** — `~/.scion/settings.yaml` → `profiles.<name>.env`
3. **Profile harness_overrides** — `~/.scion/settings.yaml` → `profiles.<name>.harness_overrides.<name>`
4. **On-disk harness-config** — `~/.scion/harness-configs/<name>/config.yaml`
5. **Template harness-config** — `<template>/harness-configs/<name>/config.yaml`
6. **Inline config** — `--config` flag or `scion-agent.json`
7. **Harness implementation** — compiled Go harness (claude, opencode, gemini, etc.)

## Field Inventory

### HarnessConfigEntry Fields (settings_v1.go:541-566)
These are the fields available in settings `harness_configs` and on-disk harness-config `config.yaml`:

| Field | Type | Purpose |
|---|---|---|
| `Harness` | string | Harness type (claude, opencode, gemini, etc.) |
| `Image` | string | Container image to use |
| `User` | string | Container user (uid/gid) |
| `Model` | string | Default model identifier |
| `TaskFlag` | string | CLI flag for task delivery |
| `Args` | []string | Command line arguments |
| `Env` | map[string]string | Environment variables |
| `Volumes` | []VolumeMount | Volume mounts |
| `AuthSelectedType` | string | Selected auth method |
| `Secrets` | []RequiredSecret | Required file secrets |
| `Provisioner` | *HarnessProvisionerConfig | Container-side provisioning config |
| `ConfigDir` | string | Harness config directory name |
| `SkillsDir` | string | Skills directory name |
| `InterruptKey` | string | Terminal interrupt key |
| `InstructionsFile` | string | Agent instructions file path |
| `SystemPromptFile` | string | System prompt file path |
| `SystemPromptMode` | string | How system prompt is injected |
| `Command` | *HarnessCommandConfig | Command structure (base, resume, task position) |
| `EnvTemplate` | map[string]string | Template-based env var generation |
| `Capabilities` | *HarnessAdvancedCapabilities | Feature support matrix |
| `Auth` | *HarnessAuthMetadata | Auth type metadata and autodetect |
| `MCP` | *HarnessMCPConfig | MCP server configuration |
| `Dialect` | map[string]interface{} | Harness dialect metadata |

### ScionConfig Fields (api/types.go:414-455)
These are the fields in the final agent config (`scion-agent.json`), which flows into the container:

| Field | Type | Purpose |
|---|---|---|
| `Harness` | string | Resolved harness name |
| `HarnessConfig` | string | Resolved harness-config name |
| `ConfigDir` | string | Config directory |
| `Env` | map[string]string | Environment variables |
| `Volumes` | []VolumeMount | Volume mounts |
| `Detached` | *bool | Detached launch mode |
| `CommandArgs` | []string | Command arguments |
| `TaskFlag` | string | Task delivery flag |
| `Model` | string | Model identifier |
| `Kubernetes` | *KubernetesConfig | K8s-specific settings |
| `AuthSelectedType` | string | Auth method selection |
| `Resources` | *ResourceSpec | Resource limits |
| `Image` | string | Container image |
| `Services` | []ServiceSpec | Sidecar service definitions |
| `MCPServers` | map[string]MCPServerConfig | MCP server configuration |
| `MaxTurns` | int | Max turn limit |
| `MaxModelCalls` | int | Max model call limit |
| `MaxDuration` | string | Max duration string |
| `Hub` | *AgentHubConfig | Hub connection settings |
| `Telemetry` | *TelemetryConfig | Telemetry settings |
| `Secrets` | []RequiredSecret | Required file secrets |
| `AgentInstructions` | string | Agent instructions content |
| `SystemPrompt` | string | System prompt content |
| `User` | string | Container user |
| `Task` | string | Task string (creation-time record) |
| `Branch` | string | Branch name (creation-time record) |
| `Info` | *AgentInfo | Agent metadata |

## Merge Points

### 1. ResolveHarnessConfig (settings_v1.go:38-83)
**Input:** settings harness_configs + profile env + profile harness_overrides
**Output:** HarnessConfigEntry

| Field | Copied from base? | Profile env merged? | Override applied? |
|---|---|---|---|
| `Harness` | Yes | No | No |
| `Image` | Yes | No | Yes (line 66) |
| `User` | Yes | No | Yes (line 69) |
| `Model` | Yes | No | No |
| `TaskFlag` | Yes | No | No |
| `Args` | Yes | No | No |
| `Env` | Yes | Yes (line 54) | Yes (line 75) |
| `Volumes` | Yes | Yes (line 59) | Yes (line 78) |
| `AuthSelectedType` | Yes | No | Yes (line 72) |
| `Secrets` | Yes | No | No |
| `Provisioner` | Yes | No | No |
| `ConfigDir` | Yes | No | No |
| `SkillsDir` | Yes | No | No |
| `InterruptKey` | Yes | No | No |
| `InstructionsFile` | Yes | No | No |
| `SystemPromptFile` | Yes | No | No |
| `SystemPromptMode` | Yes | No | No |
| `Command` | Yes | No | No |
| `EnvTemplate` | Yes | No | No |
| `Capabilities` | Yes | No | No |
| `Auth` | Yes | No | No |
| `MCP` | Yes | No | No |
| `Dialect` | Yes | No | No |

**Only `Env`, `Volumes`, `Image`, `User`, and `AuthSelectedType` are merged from profile overrides. Everything else is pass-through from base (settings harness_configs).**

### 2. Start opts.Env Merge (run.go:399-420)
**Input:** ResolveHarnessConfig result → opts.Env (for auth gathering)

| Field | Copied? |
|---|---|
| `Env` | Yes (line 403) |
| Everything else | N/A — only env vars are injected |

This merge happens BEFORE auth gathering so that `GatherAuthWithEnv` can see credentials from settings.

### 3. ProvisionAgent: On-Disk Harness-Config → finalScionCfg (provision.go:526-550)
**Input:** On-disk harness-config (global or template-level) → finalScionCfg
**Priority:** harness-config is base, template is override

| Field | Copied from harness-config? |
|---|---|
| `Image` | Yes (line 528-530) |
| `Model` | Yes (line 531-533) |
| `Args` (CommandArgs) | Yes (line 534-536) |
| `TaskFlag` | Yes (line 537-539) |
| `Env` | Yes (line 540-542) |
| `Volumes` | Yes (line 543-545) |
| `AuthSelectedType` | Yes (line 546-548) |
| `Harness` | Yes (line 523, re-applied after merge) |
| `HarnessConfig` | Yes (line 524, re-applied after merge) |
| `ConfigDir` | No |
| `User` | No |
| `SkillsDir` | No |
| `InterruptKey` | No |
| `InstructionsFile` | No |
| `SystemPromptFile` | No |
| `SystemPromptMode` | No |
| `Command` | No |
| `EnvTemplate` | No |
| `Capabilities` | No |
| `Auth` | No |
| `MCP` | No |
| `Dialect` | No |
| `Secrets` | No |
| `Provisioner` | No |
| `Telemetry` | No |
| `Resources` | No |
| `Detached` | No |
| `AgentInstructions` | No |
| `SystemPrompt` | No |
| `MaxTurns` | No |
| `MaxModelCalls` | No |
| `MaxDuration` | No |

Note: `User` is NOT copied into `finalScionCfg` here. It is resolved separately in `run.go:251-252` as part of the `unixUsername` variable resolution chain.

### 4. ProvisionAgent: Settings → finalScionCfg (provision.go:719-760)
**Input:** ResolveHarnessConfig result (settings) as base, finalScionCfg (from harness-config/template) as override
**Priority:** template/harness-config overrides settings

| Field | Copied from settings? |
|---|---|
| `Env` | Yes (line 724-726) |
| `Volumes` | Yes (line 727-729) |
| `AuthSelectedType` | Yes (line 730-732) |
| `Telemetry` | Yes (line 733-735) |
| `Resources` | Yes (lines 746-760, via profile-level merge) |
| `MaxTurns` | Yes (line 766, via default from settings) |
| `MaxModelCalls` | Yes (line 769, via default from settings) |
| `MaxDuration` | Yes (line 772, via default from settings) |
| `Model` | **No** |
| `Image` | **No** |
| `Args` (CommandArgs) | **No** |
| `TaskFlag` | **No** |
| `ConfigDir` | **No** |
| `User` | **No** |
| `SkillsDir` | **No** |
| `InterruptKey` | **No** |
| `InstructionsFile` | **No** |
| `SystemPromptFile` | **No** |
| `SystemPromptMode` | **No** |
| `Command` | **No** |
| `EnvTemplate` | **No** |
| `Capabilities` | **No** |
| `Auth` | **No** |
| `MCP` | **No** |
| `Dialect` | **No** |
| `Secrets` | **No** |
| `Provisioner` | **No** |
| `Detached` | No |
| `AgentInstructions` | No |
| `SystemPrompt` | No |

### 5. MergeScionConfig (templates.go:625-764)
**Input:** base + override → result
**Pattern:** shallow copy of base, then override fields win (with selective field-by-field handling)

| Field | Override wins? | Merge behavior |
|---|---|---|
| `Harness` | Yes | Direct assignment |
| `HarnessConfig` | Yes | Direct assignment |
| `ConfigDir` | Yes | Direct assignment |
| `Env` | Yes | Map merge (base keys preserved, override overwrites) |
| `Volumes` | Yes | Append (both kept, override appended) |
| `Detached` | Yes | Direct assignment |
| `CommandArgs` | Yes | Direct assignment |
| `TaskFlag` | Yes | Direct assignment |
| `Model` | Yes | Direct assignment |
| `Kubernetes` | Yes | Deep merge |
| `Resources` | Yes | MergeResourceSpec |
| `AuthSelectedType` | Yes | Direct assignment |
| `Image` | Yes | Direct assignment |
| `Services` | Yes | Direct assignment |
| `MCPServers` | Yes | Map merge (base keys preserved, override overwrites) |
| `MaxTurns` | Yes | Direct assignment |
| `MaxDuration` | Yes | Direct assignment |
| `Hub` | Yes | Shallow merge (Endpoint) |
| `Telemetry` | Yes | mergeTelemetryConfig |
| `AgentInstructions` | Yes | Direct assignment |
| `SystemPrompt` | Yes | Direct assignment |
| `DefaultHarnessConfig` | Yes | Direct assignment |
| `User` | Yes | Direct assignment |
| `Task` | Yes | Direct assignment |
| `Branch` | Yes | Direct assignment |
| `MaxModelCalls` | Yes | Direct assignment |
| `Info` | Yes | Deep merge (field-by-field) |

### 6. buildAgentEnv (run.go:1059-1103)
**Input:** finalScionCfg.Env + opts.Env → container environment

| Source | Priority |
|---|---|
| finalScionCfg.Env | Base |
| opts.Env | Override (wins on key conflicts) |

### 7. applyResolvedAuth (common.go:626-662)
**Input:** ResolvedAuth.EnvVars → container environment

| Source | Priority |
|---|---|
| ResolvedAuth.EnvVars | Injected BEFORE config.Env, so **config.Env wins over auth** |

### 8. ResolvedSecrets env injection (common.go:318-322)
**Input:** config.ResolvedSecrets (environment-type) → container environment

| Source | Priority |
|---|---|
| ResolvedSecrets env | Injected AFTER config.Env, so **wins over all env vars** |

## The Full Precedence Chain

### For env vars (most-well-documented path):

```
finalScionCfg.Env (lowest — built from on-disk → template → inline → settings)
  → opts.Env from settings merge (run.go:399-420, wins over finalScionCfg.Env)
  → ResolvedAuth.EnvVars (injected at common.go:265)
  → config.Env applied at common.go:308 (wins over ResolvedAuth.EnvVars)
  → ResolvedSecrets env-type (common.go:318, highest — wins over all)
```

**Critical:** Auth env vars do NOT have highest priority. The `config.Env` line at common.go:308 runs after `applyResolvedAuth` at line 265, so `finalScionCfg.Env` (which includes inline/template/on-disk/settings merged env vars) will override auth credentials. This is a potential security issue — if an auth method sets `ANTHROPIC_API_KEY` and a harness-config also defines `ANTHROPIC_API_KEY`, the harness-config value wins.

### For scalar fields (Model, Image, Args, TaskFlag, User, ConfigDir, etc.):

```
On-disk harness-config (base)
  → Template harness-config (override)
  → Inline config (override)
  → Settings harness_configs (NEVER REACHED for scalars — merge at provision.go:721-738
     does not copy these fields into settingsCfg)
```

**Note on the settings merge:** Settings ARE merged via `MergeScionConfig(settingsCfg, finalScionCfg)` at provision.go:738, but `settingsCfg` only contains `Env`, `Volumes`, `AuthSelectedType`, and `Telemetry`. Since `MergeScionConfig` copies fields from the override (`finalScionCfg`) when set, the scalar fields from settings (Model, Image, etc.) are never placed on `settingsCfg` and thus never participate in the merge.

### For User specifically (special case — resolved outside finalScionCfg):

```
run.go:210  → default "root"
run.go:251  → on-disk harness-config User
run.go:266  → settings User (from ResolveHarnessConfig, OVERRIDES on-disk!)
run.go:300  → finalScionCfg.User (OVERRIDES settings!)
```

**Inconsistency detected:** The `run.go` resolution block applies settings User AFTER on-disk User (line 266 overrides line 252), but the `finalScionCfg` merge chain applies on-disk AFTER settings (because on-disk is the base and template/inline override it). So for the `unixUsername` variable used in `RunConfig`, settings wins over on-disk — but for `finalScionCfg.User`, on-disk wins over settings. This is an inconsistency: `run.go:300` sets `unixUsername` from `finalScionCfg.User` LAST, which means `finalScionCfg.User` (inline > template > on-disk) is the effective priority for the runtime, but the intermediate `run.go:266` step already overrode on-disk with settings.

The final effective User priority is: **inline > template > on-disk** (via `finalScionCfg`). The settings User at `run.go:266` is effectively overridden by `finalScionCfg.User` at `run.go:300`.

## The Full Merge Flow (provision.go)

```
1. Inline config → finalScionCfg (starting point)
2. On-disk harness-config → MergeScionConfig(hcCfg, finalScionCfg)
   → finalScionCfg = inline > template > on-disk
3. Settings merge → MergeScionConfig(settingsCfg, finalScionCfg)
   → finalScionCfg = inline > template > on-disk > settings (for scalars NOT in settingsCfg)
   → For Env/Volumes: settings merged as base, finalScionCfg overrides
4. Profile resources → direct assignment to finalScionCfg.Resources
5. Harness-override resources → direct assignment to finalScionCfg.Resources
6. Default limits (MaxTurns, MaxModelCalls, MaxDuration, Resources) → set only if not already present
```

## Missing Fields Summary

The settings merge at `provision.go:721-738` only copies 4 fields from the resolved settings harness config:

| Field | In ResolveHarnessConfig? | Copied in provision merge? | Copied in on-disk merge? |
|---|---|---|---|
| `Env` | Yes | Yes | Yes |
| `Volumes` | Yes | Yes | Yes |
| `AuthSelectedType` | Yes | Yes | Yes |
| `Telemetry` | Yes (via ConvertV1TelemetryToAPI) | Yes | No |
| `Model` | Yes | **No** | Yes |
| `Image` | Yes | **No** | Yes |
| `Args` | Yes | **No** | Yes |
| `TaskFlag` | Yes | **No** | Yes |
| `ConfigDir` | Yes | **No** | No |
| `User` | Yes | **No** | No |
| `SkillsDir` | Yes | **No** | No |
| `InterruptKey` | Yes | **No** | No |
| `InstructionsFile` | Yes | **No** | No |
| `SystemPromptFile` | Yes | **No** | No |
| `SystemPromptMode` | Yes | **No** | No |
| `Command` | Yes | **No** | No |
| `EnvTemplate` | Yes | **No** | No |
| `Capabilities` | Yes | **No** | No |
| `Auth` | Yes | **No** | No |
| `MCP` | Yes | **No** | No |
| `Dialect` | Yes | **No** | No |
| `Secrets` | Yes | **No** | No |
| `Provisioner` | Yes | **No** | No |

## Impact

Settings-level `model`, `args`, `task_flag`, `image`, `user`, `config_dir`, and all other harness-specific fields are silently ignored in `finalScionCfg`. Users who set these in `~/.scion/settings.yaml` will not see them applied. The on-disk harness-config and template harness-config are the only paths that propagate these fields.

This means:
- `harness_configs.claude.model: llama.cpp-exp/Qwen3.6-35B-A3B-MTP` in settings has no effect
- `harness_configs.claude.args: ["--some-flag"]` in settings has no effect
- `harness_configs.claude.image: custom-image:latest` in settings has no effect
- `harness_configs.claude.user: scion` in settings has no effect (for `finalScionCfg.User`)
- Profile-level `harness_overrides.claude.model` has no effect
- Profile-level `harness_overrides.claude.args` has no effect
- Profile-level `harness_overrides.claude.image` has no effect
- Profile-level `harness_overrides.claude.user` has no effect

Users must edit `~/.scion/harness-configs/claude/config.yaml` directly to change these fields.

**Note on `User` for `unixUsername`:** The `User` field from settings DOES reach the `unixUsername` variable via `run.go:266-267`, but this is then overridden by `finalScionCfg.User` at `run.go:300-301`. So settings User has no effective impact on the container user.

## Security Implication: Env Var Precedence

The env var precedence chain has a potential security issue. `ResolvedAuth.EnvVars` (auth credentials) are injected at `common.go:265`, but `config.Env` (containing harness config env vars) is applied at `common.go:308`. This means any env var defined in a harness-config or template can override auth credentials.

Example: If a harness-config defines `ANTHROPIC_API_KEY=stolen-key`, it will override the auth-resolved `ANTHROPIC_API_KEY` from the user's actual credentials. The `opts.Env` from `run.go:399-420` (which includes settings-level env vars) also participates in `buildAgentEnv` and wins over `finalScionCfg.Env`.

The correct precedence should be:
```
Auth credentials (highest — must always win)
  → ResolvedSecrets env-type (highest — secrets always win)
  → Harness-specific env (GetEnv)
  → Telemetry env
  → User-provided env vars (lowest)
```

But the current code gives user-provided env vars (from harness-config, template, inline, settings) higher priority than auth credentials.

## Resolution Options

1. **Fix the settings merge** — Add all missing fields to `provision.go:721-738` to match the on-disk harness-config merge at `provision.go:526-550`. This would make settings-level `model`, `image`, `args`, `task_flag`, `user`, `config_dir`, `skills_dir`, `interrupt_key`, and other fields actually work.

2. **Fix the env precedence** — Move `config.Env` application before `applyResolvedAuth` in `common.go`, or have `applyResolvedAuth` run after `config.Env` and ensure auth env vars cannot be overridden. Alternatively, filter `config.Env` to exclude keys that are present in `ResolvedAuth.EnvVars`.

3. **Document the gap** — Clarify that settings-level harness config only supports `env`, `volumes`, `auth_selectedType`, and `telemetry`, and direct users to edit on-disk harness-configs for other fields.

4. **Deprecate the fields** — Remove `model`, `args`, `task_flag`, etc. from `HarnessConfigEntry` if they're not intended for settings-level configuration.

## Additional Context: EnvTemplate

`EnvTemplate` is defined in `HarnessConfigEntry` but NOT in `ScionConfig`. It is used directly from the `HarnessConfigEntry` via `harness.Resolve()` in the harness layer (see `pkg/harness/container_script_harness.go:171` and `pkg/harness/declarative_generic.go:115`). This means `EnvTemplate` is processed at harness resolution time, not through the `finalScionCfg` merge chain. It works correctly for settings-level harness configs because `ResolveHarnessConfig` returns the full `HarnessConfigEntry` with `EnvTemplate` intact.

## Additional Context: Where User is Used

`User` is resolved in two separate places:

1. **`run.go:210-302`** — The `unixUsername` variable, built through the resolution chain (default → on-disk → settings → finalScionCfg), passed to `RunConfig.UnixUsername`. This is used for:
   - Container user identification
   - Home directory path calculation
   - Auth file path expansion

2. **`finalScionCfg.User`** — Stored in `scion-agent.json` for persistence. Read from `finalScionCfg` at `run.go:300-301` to override `unixUsername`.

The two paths are consistent (finalScionCfg.User is always the last step), but the intermediate `run.go:266` step creates a misleading resolution order where settings appears to override on-disk for User.

## Additional Context: Image Resolution

`Image` follows a similar pattern to `User` but with an additional settings override layer:

1. **`run.go:247-248`** — on-disk harness-config Image
2. **`run.go:262-263`** — settings Image (OVERRIDES on-disk!)
3. **`run.go:307-308`** — finalScionCfg.Image (OVERRIDES settings!)
4. **`run.go:315-319`** — Image registry rewrite (applied to whatever was resolved)

So the effective image priority is: **inline > template > on-disk > settings** (before registry rewrite). The settings Image at `run.go:262` is intermediate — overridden by `finalScionCfg.Image` at `run.go:307`.
