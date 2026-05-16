# OpenCode Plugin Support for Scion

## Problem Statement

OpenCode is used as a harness in Scion (via `pkg/harness/opencode.go`), but there is **no bridge** between OpenCode's plugin event system and Scion's status/event infrastructure. When an agent runs OpenCode inside a Scion container, the Scion Hub has no visibility into what OpenCode is doing. This breaks the core Scion observability model.

Specifically:
- The `sciontool hook` system expects normalized events (`tool-start`, `tool-end`, `model-start`, `agent-start`, etc.) from harnesses like Claude Code and Gemini CLI
- OpenCode has no built-in hook dialect — it is not configured as one in `pkg/sciontool/hooks/dialects/`
- OpenCode's plugin system (TypeScript hooks in the OpenCode process) fires events like `tool.execute.before`, `session.status`, `message.updated` — but these never reach Scion's `agent-info.json` or Hub API
- The OpenCode harness in Scion declares `limits.max_turns: no` and `limits.max_model_calls: no` because "this harness has no hook dialect for turn events"
- The OpenCode harness declares `telemetry.native_emitter: no` because "native telemetry forwarding is not wired"

## Current Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ Scion Container                                               │
│                                                               │
│  PID 1: sciontool init                                        │
│    ├── pre-start hooks → StatusHandler → agent-info.json     │
│    ├── spawns: opencode --prompt "task"                       │
│    │                                                     │
│    │  OpenCode process:                                   │
│    │    ├── Plugin system (TypeScript hooks)               │
│    │    │    ├── tool.execute.before/after                │
│    │    │    ├── session.status / session.idle            │
│    │    │    ├── message.updated / message.removed        │
│    │    │    ├── permission.asked / permission.replied    │
│    │    │    └── ... (20+ event types)                     │
│    │    │                                               │
│    │    └── LLM interaction (Claude/GPT via API)          │
│    │         (NO hook events emitted)                      │
│    │                                                     │
│    └── (no sciontool hook invocations) → agent-info.json   │
│         stays at "running/working" forever                  │
│                                                               │
│  No sciontool hook process running                            │
│  No agent-info.json updates                                   │
│  No Hub status updates                                        │
└─────────────────────────────────────────────────────────────┘
```

## Gap Analysis

### Gap 1: No Hook Dialect for OpenCode

Scion's `sciontool hook` system has dialect parsers for Claude (`claude.go`), Gemini (`gemini.go`), and Codex (`codex.go`). Each dialect:
1. Reads raw JSON from stdin
2. Extracts the event name from harness-specific fields
3. Normalizes it to a standard event name (e.g., `PreToolUse` → `tool-start`)
4. Extracts tool name, prompt, success/error, token usage

OpenCode has no such parser. There is no `opencode.go` dialect file. This means even if we could get events out of OpenCode, `sciontool hook --dialect opencode` would fail.

### Gap 2: No Status Reporting Pipeline

The Claude/Gemini/Codex harnesses emit structured hook events via their respective CLI interfaces (Claude Code's `--hook-event-name`, Gemini's `--logger`, etc.). These events are piped to `sciontool hook` which processes them through a handler chain:

```
sciontool hook (stdin JSON)
  → Dialect Parser (normalize event)
  → StatusHandler (write agent-info.json)
  → LoggingHandler (write agent.log)
  → PromptHandler (save prompt.md)
  → HubHandler (POST to Hub API)
  → LimitsHandler (track turns/calls)
  → TelemetryHandler (OTel spans)
```

OpenCode does not invoke `sciontool hook` at any point. The OpenCode harness's `GetCommand()` returns just `["opencode", "--prompt", "task"]` — no hook piping.

### Gap 3: No Heartbeat Mechanism

Scion's Hub requires heartbeats to:
- Keep agents alive in the database
- Detect stale/offline agents
- Support stalled detection (5-minute threshold on `last_activity_event`)

Claude/Gemini/Codex harnesses emit frequent `tool-start`/`tool-end`/`model-start`/`model-end` events which serve as activity heartbeats. OpenCode emits nothing, so agents would be marked `stalled` after 5 minutes and eventually `offline`.

### Gap 4: No Limits Tracking

The OpenCode harness declares `max_turns: no` and `max_model_calls: no` because there is no hook dialect to extract turn/model-call events. The `LimitsHandler` relies on `agent-end` and `model-end` events to increment counters. Without these, turn and model-call limits cannot be enforced.

### Gap 5: No Assistant Text Forwarding

When Claude/Gemini sessions end, the `HubHandler` forwards `agent-end` assistant text to the Hub as outbound messages (up to 64KB). This populates the Messages tab in the web UI. OpenCode has no equivalent mechanism.

### Gap 6: No Permission-to-Input Bridge

Scion's `WAITING_FOR_INPUT` state enables the `scion attach` workflow where a human can intervene. This is triggered by `notification` events or Claude-specific `ExitPlanMode`/`AskUserQuestion` tool events. OpenCode's `permission.asked` event has no equivalent in the Scion status model.

## Proposed Solutions

### Option A: OpenCode Plugin (Client-Side)

Write a TypeScript plugin for OpenCode that hooks into OpenCode's event system and bridges to Scion's `sciontool hook` system.

**Pros:**
- Uses OpenCode's native plugin mechanism (`~/.config/opencode/plugins/` or `.opencode/plugins/`)
- No changes to OpenCode core needed
- Can respond to rich event set (messages, permissions, LSP, etc.)
- Follows OpenCode's plugin patterns (similar to notification.js example)

**Cons:**
- Runs in the OpenCode process, not as a separate harness-level component
- Must shell out to `sciontool hook` (via `$` shell API) or call Hub API directly
- Plugin lifecycle tied to OpenCode session (no pre-start/post-start lifecycle hooks)
- Limited by what OpenCode's plugin events expose (may not see raw LLM I/O)
- Must handle auth token management for Hub API

### Option B: Harness Wrapper (Server-Side)

Modify the OpenCode harness (`pkg/harness/opencode.go`) to wrap the `opencode` command and pipe its output through `sciontool hook`. This would require OpenCode to emit structured events to stdout/stderr.

**Pros:**
- Same architecture as Claude/Gemini/Codex harnesses
- Clean separation: harness emits, sciontool consumes
- No dependency on OpenCode plugin system

**Cons:**
- Requires changes to OpenCode core to emit hook events (not currently possible)
- Tightly couples Scion to OpenCode internals
- Cannot access rich plugin events (permission prompts, LSP diagnostics, etc.)

### Option C: Hybrid (Recommended)

**Phase 1:** Create an OpenCode plugin that bridges OpenCode events → `sciontool hook` stdin. This handles the common case of agents running OpenCode inside Scion containers.

**Phase 2:** Add an OpenCode hook dialect to `sciontool/hooks/dialects/` that can parse events emitted by the plugin (or by a future OpenCode native hook mode).

**Phase 3:** Add native hook emission support to the Scion OpenCode image (via an environment variable or CLI flag on `opencode`), making the plugin optional for new installations.

## Detailed Implementation Plan — Option C (Hybrid)

### Phase 1: OpenCode Plugin (Status Bridge)

Create a TypeScript plugin that runs inside OpenCode and bridges events to Scion.

#### 1.1 Plugin Structure

```
pkg/harness/opencode/embeds/opencode-plugin.js
(or injected into ~/.config/opencode/plugins/scion-status.js)
```

```typescript
export const ScionStatusPlugin = async ({ project, client, $, directory, worktree }) => {
  // On session start, register with Scion
  const agentId = process.env.SCION_AGENT_ID || ""
  const hubEndpoint = process.env.SCION_HUB_ENDPOINT || process.env.SCION_HUB_URL || ""
  
  return {
    // Session lifecycle
    "session.created": async () => {
      // Signal to Scion that session has started
      await $`sciontool hook <<< '{"name":"session-start","data":{}}'`
    },
    
    // Tool execution events
    "tool.execute.before": async (input) => {
      await $`sciontool hook <<< '{"name":"tool-start","data":{"tool_name":"${input.tool}"}}'`
    },
    "tool.execute.after": async (input, output) => {
      await $`sciontool hook <<< '{"name":"tool-end","data":{"tool_name":"${input.tool}","success":${output?.success ?? true}}}'`
    },
    
    // Message events (map to model/agent events)
    "message.updated": async ({ event }) => {
      if (event?.role === "user") {
        await $`sciontool hook <<< '{"name":"prompt-submit","data":{"prompt":"${event.content?.slice(0,100)}"}}'`
      } else if (event?.role === "assistant") {
        // Could map to model-start/model-end
      }
    },
    
    // Permission events → waiting_for_input
    "permission.asked": async (input) => {
      await $`sciontool hook <<< '{"name":"notification","data":{"message":"${input.description?.slice(0,100)}"}}'`
    },
    "permission.replied": async (input) => {
      // Tool will execute next, no special handling needed
    },
    
    // Session end
    "session.deleted": async () => {
      await $`sciontool hook <<< '{"name":"session-end","data":{}}'`
    },
    
    // Session idle/error
    "session.idle": async () => {
      await $`sciontool hook <<< '{"name":"response-complete","data":{}}'`
    },
    "session.error": async ({ error }) => {
      await $`sciontool hook <<< '{"name":"tool-start","data":{"tool_name":"error","error":"${error?.slice(0,200)}"}}'`
      await $`sciontool hook <<< '{"name":"session-end","data":{}}'`
    },
  }
}
```

#### 1.2 Plugin Deployment

The plugin must be injected into the OpenCode container's config directory during provisioning. Two approaches:

**A. Embed in opencode embeds:**
```
pkg/harness/opencode/embeds/scion-plugin.js
```
During `Provision()`, copy into `~/.config/opencode/plugins/scion-plugin.js`.

**B. Use OpenCode's npm plugin mechanism:**
Publish the plugin to an internal npm scope and reference it in the embedded `opencode.json`:
```json
{
  "$schema": "https://opencode.ai/config.json",
  "theme": "matrix",
  "plugin": ["@scion/opencode-plugin-status"]
}
```

Approach A is simpler and doesn't require npm infrastructure.

#### 1.3 Environment Variables

The plugin needs Scion context. These are already set in Scion containers:
- `SCION_AGENT_ID` — agent identifier for Hub API
- `SCION_HUB_ENDPOINT` / `SCION_HUB_URL` — Hub URL
- `HOME` — for `agent-info.json` location

The plugin should check `SCION_AGENT_ID` to determine if it should activate (only run inside Scion containers).

### Phase 2: OpenCode Hook Dialect

Create a dialect parser for `sciontool hook` that can parse events emitted by the OpenCode plugin.

#### 2.1 Dialect File

```
pkg/sciontool/hooks/dialects/opencode.go
```

The OpenCode dialect is the simplest of all — it uses the normalized event format directly (since the plugin emits pre-normalized JSON):

```go
package dialects

import "github.com/GoogleCloudPlatform/scion/pkg/sciontool/hooks"

type OpenCode struct{}

func (o *OpenCode) Name() string { return "opencode" }

func (o *OpenCode) Parse(data map[string]interface{}) (*hooks.Event, error) {
    name, _ := data["name"].(string)
    rawData, _ := data["data"].(map[string]interface{})
    
    event := &hooks.Event{
        Name:    name,
        RawName: name,
        Dialect: "opencode",
        Data:    parseEventData(rawData),
    }
    return event, nil
}

func parseEventData(raw map[string]interface{}) hooks.EventData {
    // Direct field mapping — plugin already emits normalized format
    return hooks.EventData{
        Prompt:     stringOrEmpty(raw["prompt"]),
        ToolName:   stringOrEmpty(raw["tool_name"]),
        Message:    stringOrEmpty(raw["message"]),
        ToolInput:  stringOrEmpty(raw["tool_input"]),
        ToolOutput: stringOrEmpty(raw["tool_output"]),
        FilePath:   stringOrEmpty(raw["file_path"]),
        Success:    boolOrFalse(raw["success"]),
        Error:      stringOrEmpty(raw["error"]),
        Raw:        raw,
    }
}
```

#### 2.2 Hook Command Integration

Update `cmd/sciontool/commands/hook.go` to accept `--dialect opencode`:

```go
var dialectFlag = "claude" // existing default

// In RunE:
dialects := map[string]hooks.Dialect{
    "claude":  &dialects.Claude{},
    "gemini":  &dialects.Gemini{},
    "codex":   &dialects.Codex{},
    "opencode": &dialects.OpenCode{},  // NEW
}
```

### Phase 3: Harness Integration

#### 3.1 Update OpenCode Harness

Modify `pkg/harness/opencode.go` to:

1. **Inject the plugin during provisioning** (copy `scion-plugin.js` to `~/.config/opencode/plugins/`)
2. **Inject environment variables** for Scion context
3. **Pipe output through sciontool hook** (optional, for cases where the plugin isn't available)

```go
func (o *OpenCode) Provision(ctx context.Context, agentName, agentDir, agentHome, agentWorkspace string) error {
    // Existing: no-op
    // New: inject Scion plugin
    pluginsDir := filepath.Join(agentHome, o.DefaultConfigDir(), "plugins")
    if err := os.MkdirAll(pluginsDir, 0755); err != nil {
        return fmt.Errorf("creating plugins dir: %w", err)
    }
    
    embedFS, _ := o.GetHarnessEmbedsFS()
    pluginData, err := embedFS.ReadFile("embeds/scion-plugin.js")
    if err != nil {
        return fmt.Errorf("reading plugin: %w", err)
    }
    
    if err := os.WriteFile(filepath.Join(pluginsDir, "scion-plugin.js"), pluginData, 0644); err != nil {
        return fmt.Errorf("writing plugin: %w", err)
    }
    
    return nil
}
```

#### 3.2 Update Capabilities Declaration

Update `pkg/harness/opencode/embeds/config.yaml` to reflect new capabilities:

```yaml
capabilities:
  limits:
    max_turns: { support: "yes", reason: "Supported via OpenCode hook dialect" }
    max_model_calls: { support: "yes", reason: "Supported via OpenCode hook dialect" }
    max_duration: { support: "yes" }
  telemetry:
    enabled: { support: "yes" }
    native_emitter: { support: "yes", reason: "Forwarded via OpenCode hook dialect" }
```

And update `pkg/harness/opencode.go` `AdvancedCapabilities()`:

```go
func (o *OpenCode) AdvancedCapabilities() api.HarnessAdvancedCapabilities {
    return api.HarnessAdvancedCapabilities{
        Harness: "opencode",
        Limits: api.HarnessLimitCapabilities{
            MaxTurns:      api.CapabilityField{Support: api.SupportYes},
            MaxModelCalls: api.CapabilityField{Support: api.SupportYes},
            MaxDuration:   api.CapabilityField{Support: api.SupportYes},
        },
        Telemetry: api.HarnessTelemetryCapabilities{
            EnabledConfig: api.CapabilityField{Support: api.SupportYes},
            NativeEmitter: api.CapabilityField{Support: api.SupportYes},
        },
        // ... rest unchanged
    }
}
```

### Phase 4: Enhanced Event Mapping

Map more OpenCode plugin events to Scion hook events for richer observability.

#### 4.1 Event Mapping Table

| OpenCode Plugin Event | Scion Hook Event | Activity | Notes |
|---|---|---|---|
| `session.created` | `session-start` | `working` | Clears sticky |
| `session.idle` | `response-complete` | `completed` | Session finished |
| `session.error` | `session-end` | `stopped` | With error detail |
| `tool.execute.before` | `tool-start` | `executing` | With tool name |
| `tool.execute.after` | `tool-end` | `working` | With success/error |
| `message.updated` (user) | `prompt-submit` | `thinking` | With prompt text |
| `message.updated` (assistant) | `model-start` → `model-end` | `thinking` → `working` | Per-message-chunk |
| `permission.asked` | `notification` | `waiting_for_input` | **Sticky** |
| `permission.replied` | (none) | — | Next tool-start clears it |
| `lsp.client.diagnostics` | (none) | — | Future: observability |
| `tui.toast.show` | (none) | — | Future: notifications |
| `shell.env` | (none) | — | Future: env injection |

#### 4.2 Heartbeat Strategy

Since OpenCode doesn't emit per-LLM-call events like Claude/Gemini, implement a heartbeat mechanism:

1. **Event-driven heartbeat:** Every `tool.execute.before`/`tool.execute.after`/`message.updated` event triggers a `sciontool hook` call
2. **Timer-based heartbeat:** If no events fire for >60 seconds, send a `sciontool hook <<< '{"name":"tool-start","data":{"tool_name":"_heartbeat"}}'` followed by `tool-end`
3. **Session heartbeat:** On `session.idle`, send `response-complete` to mark completion

The timer-based heartbeat prevents agents from being marked `stalled` during long LLM generation periods where no tool events fire.

### Phase 5: Hub API Direct Integration (Fallback)

For cases where shelling out to `sciontool hook` is unreliable, the plugin can also call the Hub API directly using `fetch()`:

```typescript
export const ScionStatusPlugin = async ({ client, $ }) => {
  const hubEndpoint = process.env.SCION_HUB_ENDPOINT || process.env.SCION_HUB_URL || ""
  const agentId = process.env.SCION_AGENT_ID || ""
  const tokenPath = `${process.env.HOME}/.scion/scion-token`
  
  // Read token from file (same as sciontool hub client)
  const readToken = async () => {
    const fs = await import("fs")
    try {
      return fs.readFileSync(tokenPath, "utf-8").trim()
    } catch {
      return process.env.SCION_AUTH_TOKEN || ""
    }
  }
  
  const updateStatus = async (activity: string, message?: string, toolName?: string) => {
    const token = await readToken()
    if (!hubEndpoint || !agentId || !token) return
    
    await client.app.log({
      body: {
        service: "scion-status",
        level: "debug",
        message: `Updating status: ${activity}`,
      },
    })
    
    // Direct HTTP call to Hub API
    // POST /api/v1/agents/{agentId}/status
    // Header: X-Scion-Agent-Token: <token>
    // Body: { activity, toolName, message, status: DisplayStatus() }
  }
  
  return {
    "tool.execute.before": async (input) => {
      await updateStatus("executing", `Executing: ${input.tool}`, input.tool)
    },
    // ...
  }
}
```

This dual-path approach (shelling to `sciontool hook` + direct Hub API) ensures robustness:
- `sciontool hook` path: writes `agent-info.json` (local broker can read it) AND sends to Hub
- Direct Hub API path: sends to Hub even if `sciontool` is unavailable

### File Changes Summary

| File | Action | Description |
|---|---|---|
| `pkg/harness/opencode/embeds/scion-plugin.js` | **New** | OpenCode plugin that bridges events to Scion |
| `pkg/sciontool/hooks/dialects/opencode.go` | **New** | Dialect parser for OpenCode events |
| `cmd/sciontool/commands/hook.go` | **Modify** | Register `opencode` dialect |
| `pkg/harness/opencode.go` | **Modify** | Inject plugin during Provision(), update capabilities |
| `pkg/harness/opencode/embeds/config.yaml` | **Modify** | Update capabilities to `support: yes` for limits/telemetry |
| `pkg/harness/opencode/embeds/opencode.json` | **Modify** | Optionally add plugin reference |

## Risks and Tradeoffs

### Risk 1: Plugin Reliability

OpenCode's plugin system may have lifecycle issues (e.g., plugins not loaded, errors silently swallowed). The plugin should be defensive:
- Check `SCION_AGENT_ID` before doing anything (graceful no-op outside Scion)
- Wrap all `$` calls in try/catch (shelling out can fail)
- Use `client.app.log()` for structured logging (not `console.log`)
- Debounce rapid events (tool execution can fire many times per second)

### Risk 2: Performance Overhead

Shelling out to `sciontool hook` for every tool event adds subprocess spawn overhead. Mitigations:
- Batch rapid events (debounce 100ms window)
- Use `sciontool hook --data` subcommand for simple status updates (no JSON stdin parsing)
- Consider a long-lived `sciontool hook` process that reads from a pipe (future optimization)

### Risk 3: Event Granularity

OpenCode's plugin events don't map 1:1 to Scion's hook events. For example:
- OpenCode has no `model-start`/`model-end` — these are internal to the LLM client
- OpenCode's `message.updated` fires per-chunk (streaming), which could cause rapid activity flapping
- The `thinking` activity (model-start) may never fire, causing agents to go straight from `working` to `executing`

Mitigation: Infer `model-start`/`model-end` from OpenCode message events:
- First assistant message chunk → `model-start` (thinking)
- Last assistant message chunk → `model-end` (working)
- User message → `prompt-submit` (thinking)

### Risk 4: Auth Token Management

The plugin needs the Scion auth token to call the Hub API. The token is stored at `~/.scion/scion-token` inside the container. The plugin reads this on each call (or caches it). Token refresh is handled by `sciontool hook` path (via HubHandler's built-in refresh logic). The direct API path would need to implement refresh separately.

### Risk 5: OpenCode Version Compatibility

The plugin relies on OpenCode's plugin API, which may evolve. The plugin should:
- Check OpenCode version at startup (via `client.app.version` or similar)
- Gracefully degrade if plugin hooks are unavailable
- Be versioned and tested against specific OpenCode releases

## Open Questions

1. **Should the plugin be a separate npm package or embedded?**
   - Embedded (`scion-plugin.js` in `embeds/`) is simpler, no npm infra needed
   - npm package (`@scion/opencode-plugin-status`) allows independent versioning and community contributions
   - **Recommendation:** Start with embedded, extract to npm later

2. **Should we add a native hook emission mode to OpenCode?**
   - Adding `--scion-hooks` flag to OpenCode that pipes events to stdout in `sciontool hook` JSON format
   - Would eliminate the need for the plugin and `$` shelling
   - Requires changes to OpenCode core (may not be feasible if OpenCode is a dependency, not a fork)
   - **Recommendation:** Defer; the plugin approach works for now

3. **Should we add a `session.status` event to OpenCode's plugin system?**
   - Currently `session.status` exists but its payload is unclear
   - Could carry structured phase/activity info that maps directly to Scion's model
   - **Recommendation:** File as a feature request for OpenCode

4. **What about the `sciontool status` commands (`ask_user`, `blocked`, `task_completed`)?**
   - These are AGENTS.md instructions for AI agents to call
   - Agents would need to use the `$` shell API to invoke them
   - The plugin could auto-detect when an agent calls these and relay to Hub
   - **Recommendation:** Include in plugin as explicit tool-call hooks

## Appendix: Reference — Claude Dialect Normalization

For comparison, here's how Claude's hook events map to normalized events (the pattern the OpenCode dialect should follow, but simpler since the plugin emits pre-normalized events):

| Claude Hook | Normalized | Activity |
|---|---|---|
| `SessionStart` | `session-start` | `working` (clears sticky) |
| `UserPromptSubmit` | `prompt-submit` | `thinking` |
| `BeforeModel` | `model-start` | `thinking` |
| `AfterModel` | `model-end` | `working` |
| `PreToolUse` | `tool-start` | `executing` |
| `PostToolUse` | `tool-end` | `working` |
| `BeforeAgent` | `agent-start` | `thinking` |
| `AfterAgent` | `agent-end` | `working` |
| `Stop` | `agent-end` | `working` (+ assistant text) |
| `Notification` | `notification` | `waiting_for_input` (sticky) |
| `SessionEnd` | `session-end` | `stopped` |
| `ExitPlanMode` (tool) | `tool-start` | `waiting_for_input` (sticky) |
| `AskUserQuestion` (tool) | `tool-start` | `waiting_for_input` (sticky) |

## Appendix: Hub API Status Payload

The Hub expects this payload on `POST /api/v1/agents/{agentId}/status`:

```json
{
  "phase": "running",           // optional: created|provisioning|cloning|starting|running|suspended|stopping|stopped|error
  "activity": "thinking",       // optional: working|thinking|executing|waiting_for_input|blocked|completed|limits_exceeded|stalled|offline|crashed
  "toolName": "Bash",           // optional: only meaningful when activity=executing
  "status": "thinking",         // optional: backward-compatible display string
  "message": "Processing...",   // optional: up to ~100 chars (truncated by HubHandler)
  "taskSummary": "Done the thing", // optional: up to ~200 chars
  "heartbeat": false,           // optional: if true, only updates last_seen
  "currentTurns": 5,            // optional: tracked by LimitsHandler
  "currentModelCalls": 12,      // optional: tracked by LimitsHandler
  "startedAt": "2026-01-01T00:00:00Z", // optional
  "exitCode": 0                 // optional: on session-end
}
```

The `status` field is computed via `AgentState.DisplayStatus()` which returns `phase/activity` or just `activity` depending on phase.

## Appendix: Sticky Activity Semantics

Critical for the plugin to respect:

| Activity | Sticky? | Terminal? | Clearable By |
|---|---|---|---|
| `waiting_for_input` | Yes | No | New work events (`prompt-submit`, `agent-start`, `session-start`), tool-start (clears only this one) |
| `blocked` | Yes | No | New work events |
| `completed` | Yes | No | New work events only |
| `limits_exceeded` | Yes | Yes | New work events only |
| `crashed` | Yes | Yes | New work events only |
| `working` | No | No | Any event |
| `thinking` | No | No | Any event |
| `executing` | No | No | Any event |
| `stalled` | No | No | Any non-stalled activity event |
| `offline` | No | No | Heartbeat |

The plugin MUST NOT send `working`/`thinking`/`executing` events when the current activity is sticky (except for new work events). The `sciontool hook` StatusHandler and HubHandler both check this via `agent-info.json`, but the plugin should be defensive and check locally too to avoid unnecessary Hub API calls.
