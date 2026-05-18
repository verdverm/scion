# OpenCode Plugin Support for Scion

## Problem Statement

OpenCode is used as a harness in Scion (via `pkg/harness/opencode.go`), but the bridge between OpenCode's plugin event system and Scion's status/event infrastructure was untested. When an agent runs OpenCode inside a Scion container, the Scion Hub needs visibility into what OpenCode is doing for the core Scion observability model to work.

Specifically:
- The `sciontool hook` system expects normalized events (`tool-start`, `tool-end`, `model-start`, `agent-start`, etc.) from harnesses like Claude Code and Gemini CLI
- OpenCode has no built-in hook dialect — it is not configured as one in `pkg/sciontool/hooks/dialects/`
- OpenCode's plugin system (TypeScript hooks in the OpenCode process) fires events like `tool.execute.before`, `session.status`, `message.updated` — these need to reach Scion's `agent-info.json` or Hub API
- The OpenCode harness in Scion declares `limits.max_turns: yes` and `limits.max_model_calls: yes` (via event bridge)
- The OpenCode harness declares `telemetry.native_emitter: yes` (forwarded via event bridge)

## Current Architecture (Implemented)

```
┌─────────────────────────────────────────────────────────────┐
│ Scion Container                                               │
│                                                               │
│  PID 1: sciontool init                                        │
│    ├── pre-start hooks → sciontool harness provision (Python) │
│    │    └── copies scion-plugin.js → ~/.config/opencode/      │
│    │        plugins/scion-plugin.js                            │
│    ├── spawns: opencode --prompt "task"                       │
│    │                                                     │
│    │  OpenCode process:                                   │
│    │    ├── Plugin system (TypeScript hooks)               │
│    │    │    ├── tool.execute.before/after                │
│    │    │    ├── session.created/idle/error/deleted       │
│    │    │    ├── message.updated                          │
│    │    │    ├── permission.asked/replied                 │
│    │    │    └── ... (20+ event types)                     │
│    │    │                                               │
│    │    │  scion-plugin.js intercepts events →            │
│    │    │    writes JSON to /tmp/scion-hook-*.json        │
│    │    │    pipes to: sciontool hook --dialect=opencode   │
│    │    │                                               │
│    │    └── LLM interaction (Claude/GPT via API)          │
│    │                                                     │
│    └── sciontool hook pipeline:                           │
│         Dialect → StatusHandler → LoggingHandler          │
│         → PromptHandler → HubHandler → LimitsHandler      │
│         → TelemetryHandler                                │
│         → agent-info.json updated, Hub API called          │
└─────────────────────────────────────────────────────────────┘
```

## Gap Analysis (Resolved)

### Gap 1: No Hook Dialect for OpenCode — RESOLVED
`pkg/sciontool/hooks/dialects/opencode.go` implements `OpenCodeDialect` that parses both nested (`{"name":"...","data":{}}`) and flat JSON formats emitted by the plugin. Registered in `registry.go`. The `--dialect=opencode` flag is accepted by `sciontool hook`.

### Gap 2: No Status Reporting Pipeline — RESOLVED
The `scion-plugin.js` bridges OpenCode events to `sciontool hook --dialect=opencode` via temp files piped to stdin. The full handler chain (StatusHandler → LoggingHandler → PromptHandler → HubHandler → LimitsHandler → TelemetryHandler) processes events normally.

### Gap 3: No Heartbeat Mechanism — RESOLVED
The plugin fires `model-start` + `model-end` pairs every 45 seconds (suppressed during sticky states). This keeps `last_activity_event` fresh well under the 5-minute stalled threshold.

### Gap 4: No Limits Tracking — TURN COUNTING STILL BROKEN
The dialect and event pipeline support limits. The plugin marks heartbeat `model-start`/`model-end` events with `_scion_heartbeat: true`, and the `LimitsHandler` skips these to prevent heartbeat noise from consuming `max_model_calls` quota. **`max_model_calls` works for real model-ends.** However, OpenCode never emits `agent-end` — it sends `session-end` instead — so **`max_turns` limits are ineffective for OpenCode agents**. See Known Gaps below.

### Gap 5: No Assistant Text Forwarding — RESOLVED
The plugin now collects assistant text from `message.updated` (assistant) events and includes it in `session-end` events. The `HubHandler` forwards `assistant_text` from `session-end` via `SendOutboundMessage`, the same path as `agent-end` for Claude. **Assistant responses now appear in the Messages tab.**

### Gap 6: No Permission-to-Input Bridge — RESOLVED
`permission.asked` → `notification` → `waiting_for_input` (sticky) is fully wired. `permission.replied` is logged and the next `tool-start` clears `waiting_for_input` via the StatusHandler's sticky-clearing logic.

## Status

### What's Implemented and Working

| Component | Status | File |
|---|---|---|
| **scion-plugin.js** | Done + verified | `pkg/harness/opencode/embeds/scion-plugin.js` — Full event bridge. Debouncing (200ms tools, 2s messages), heartbeat (45s with `_scion_heartbeat` flag), assistant text collection from `message.updated` events, error logging to app logger, sticky activity awareness, 20+ event handlers. Verified loaded in running agent (shows as "1 Plugin: scion-plugin" in OpenCode UI). |
| **OpenCode dialect** | Done | `pkg/sciontool/hooks/dialects/opencode.go` — Parses nested + flat formats. Registered in `registry.go`. |
| **Hook command** | Done | `--dialect=opencode` accepted in `hook.go` help text. |
| **Provision script** | Done | `pkg/harness/opencode/embeds/provision.py` — Handles auth (api-key, auth-file, vertex-ai, none), MCP server translation, plugin injection (`_inject_scion_plugin`). |
| **Container-script harness** | Done | `config.yaml` declares `provisioner.type: container-script`. Parity tests pass. |
| **Capabilities** | Done | `max_turns: yes`, `max_model_calls: yes`, `native_emitter: yes`, `vertex_ai: yes`, `none: yes`, MCP stdio/sse/streamable-http: yes. |
| **Dialect tests** | Done | `opencode_test.go` — Flat + nested formats, heartbeat, tool success/error. |
| **Parity tests** | Done | `opencode_parity_test.go` — Embed seeding, provision staging, script integration (happy path, MCP, no-creds). |
| **Agent lifecycle** | Done + verified | Agent starts, runs tasks, Hub shows running/working status with updating lastActivityEvent. |

### Known Gaps

1. **Turn counting doesn't work (Gap 4)** — The `LimitsHandler` only increments on `agent-end` (turns) and `model-end` (model calls). The OpenCode plugin never emits `agent-end`; it sends `session-end` instead. **`max_turns` limits are ineffective for OpenCode agents.** `max_model_calls` works correctly for real model-ends; heartbeat model-ends are properly skipped via the `_scion_heartbeat` flag.

2. **No Hub API direct fallback (Phase 5 never implemented)** — The original design proposed dual-path: shell to `sciontool hook` + direct Hub API calls as fallback. Only the `sciontool hook` path exists. If `sciontool` is unavailable in the container, the plugin logs errors to the OpenCode app logger but events are lost.

3. **No `agent-end` event ever emitted** — The canonical event for turn counting and post-agent cleanup. OpenCode uses `session.deleted` → `session-end` instead. Assistant text forwarding is now handled via `session-end` (Gap 5 resolved).

4. **sciontool heartbeat fails on macOS** — `SCION_HUB_ENDPOINT` defaults to `http://localhost:9810` inside containers, which is unreachable from Docker containers on macOS (needs `host.docker.internal`). This causes repeated heartbeat errors but doesn't affect agent execution. Configurable via `server.broker.container_hub_endpoint` in `~/.scion/settings.yaml`.

### What Works vs What Doesn't — Quick Reference

| Capability | Works? | Notes |
|---|---|---|
| Status updates (agent-info.json) | Yes | All events flow through StatusHandler |
| Hub API status reporting | Yes | HubHandler processes all event types |
| Heartbeat / stalled detection | Yes | 45s model-start/model-end pairs |
| Permission → waiting_for_input | Yes | notification → sticky waiting_for_input |
| Tool execution tracking | Yes | tool-start → executing, tool-end → working |
| Message events (thinking) | Yes | message.updated (assistant) → model-start |
| User prompt tracking | Yes | message.updated (user) → prompt-submit |
| Turn counting (max_turns) | **No** | LimitsHandler needs agent-end, plugin sends session-end |
| Model call counting (max_model_calls) | **Yes** | Real model-ends count; heartbeat model-ends are skipped via `_scion_heartbeat` flag |
| Assistant text → Messages tab | **Yes** | Plugin collects assistant text from `message.updated`, HubHandler forwards from `session-end` |
| Session completion reporting | Yes | session.idle → response-complete → completed |
| Session error reporting | Yes | session.error → session-end → stopped |
| Telemetry (OTel spans) | Yes | TelemetryHandler processes events |
| Logging (agent.log) | Yes | LoggingHandler writes all events |
| Prompt capture (prompt.md) | Yes | PromptHandler saves user prompts |

## Gap Analysis (Open / Deferred)

### Gap 4: Limits Tracking — Turn counting still needs `agent-end` or `session-end` handling

**PRIORITY: High** — This is the most impactful remaining gap. Without it, `max_turns` limits have no effect on OpenCode agents.

The `LimitsHandler` in `pkg/sciontool/hooks/handlers/limits.go` only processes `agent-end` (turns) and `model-end` (model calls). Heartbeat model-ends are skipped via the `_scion_heartbeat` flag, so real model-call counting works for OpenCode. Turn counting remains broken since OpenCode never emits `agent-end`.

**Option A:** Extend LimitsHandler to also process `session-end` as a turn completion event. This would count each OpenCode session as one turn.

**Option B:** Have the plugin emit `agent-end` on `session.deleted` with collected assistant text. This provides full parity with Claude/Gemini dialects and fixes turn counting without modifying LimitsHandler.

**Option C:** Add a `model-end` event to the plugin that fires when the assistant finishes responding. This requires correlating `message.updated` events to infer LLM call boundaries. Fragile since OpenCode's message events don't carry LLM call boundaries.

### Gap 5: Assistant Text Forwarding — RESOLVED

Implemented via Option B: the plugin collects assistant text from `message.updated` (assistant) events into an `assistantTextParts` buffer, and includes it in `session-end` events. The `HubHandler` forwards `assistant_text` from `session-end` via `SendOutboundMessage`.

**Remaining:** Turn counting is still broken (see Gap 4 above — highest priority item).

## Implementation Details

### Plugin Deployment (Actual)

The plugin is **not** injected by Go's `Provision()` method (which is a no-op). Instead:

1. `scion-plugin.js` lives in `pkg/harness/opencode/embeds/` alongside `provision.py`, `config.yaml`, and `opencode.json`.
2. `SeedHarnessConfig()` walks the embed FS and places `scion-plugin.js` under `home/.config/opencode/scion-plugin.js` in the harness-config directory.
3. During agent provisioning, the container-side `provision.py` script (run as a pre-start hook via `sciontool harness provision`) copies the plugin to `~/.config/opencode/plugins/scion-plugin.js`.
4. The plugin activates when `SCION_AGENT_ID` is set in the container environment.

### Event Mapping (Actual)

| OpenCode Plugin Event | Scion Hook Event | Activity | Notes |
|---|---|---|---|
| `session.created` | `session-start` | `working` | Clears sticky |
| `session.deleted` | `session-end` | `stopped` (phase) | Stops agent, carries `assistant_text` |
| `session.idle` | `response-complete` | `completed` | Session finished |
| `session.error` | `session-end` | `stopped` (phase) | With error detail and `assistant_text` |
| `tool.execute.before` | `tool-start` | `executing` | With tool name |
| `tool.execute.after` | `tool-end` | `working` | With success/error |
| `message.updated` (user) | `prompt-submit` | `thinking` | With prompt text (debounced 2s) |
| `message.updated` (assistant) | `model-start` | `thinking` | With content preview (debounced 2s) |
| `permission.asked` | `notification` | `waiting_for_input` | **Sticky** |
| `permission.replied` | (none) | — | Logged only, next tool-start clears sticky |
| `tui.command.execute` | `prompt-submit` | `thinking` | TUI command input |
| Heartbeat (timer) | `model-start` + `model-end` | `thinking` → `working` | Every 45s, marked with `_scion_heartbeat: true`, suppressed in sticky states |

### File Changes Summary

| File | Action | Description |
|---|---|---|
| `pkg/harness/opencode/embeds/scion-plugin.js` | **New** | OpenCode plugin that bridges events to Scion |
| `pkg/sciontool/hooks/dialects/opencode.go` | **New** | Dialect parser for OpenCode events |
| `pkg/sciontool/hooks/dialects/opencode_test.go` | **New** | Dialect unit tests |
| `pkg/sciontool/hooks/dialects/registry.go` | **Modify** | Register `opencode` dialect |
| `cmd/sciontool/commands/hook.go` | **Modify** | Accept `--dialect=opencode` in help text |
| `pkg/harness/opencode/embeds/provision.py` | **New** | Container-side provisioner (auth, MCP, plugin injection) |
| `pkg/harness/opencode/embeds/config.yaml` | **Modify** | `provisioner.type: container-script`, capabilities updated to `yes` |
| `pkg/harness/opencode/embeds/opencode.json` | **Modify** | Theme set to "matrix" |
| `pkg/harness/opencode.go` | **Modify** | `AdvancedCapabilities()` updated, `none` auth type, VertexAI support |
| `pkg/harness/opencode_parity_test.go` | **New** | Harness parity and provision script integration tests |
| `pkg/sciontool/hooks/handlers/hub.go` | **Modify** | Forward assistant text from `session-end` events |
| `pkg/sciontool/hooks/handlers/limits.go` | **Modify** | Skip heartbeat events via `_scion_heartbeat` flag |
| `pkg/sciontool/hooks/handlers/hub_test.go` | **Modify** | Tests for session-end assistant text forwarding |
| `pkg/sciontool/hooks/handlers/limits_test.go` | **Modify** | Tests for heartbeat event skipping |

## Risks and Tradeoffs

### Risk 1: Plugin Reliability

OpenCode's plugin system may have lifecycle issues (e.g., plugins not loaded, errors silently swallowed). The plugin is defensive:
- Checks `SCION_AGENT_ID` before doing anything (graceful no-op outside Scion)
- Wraps all `$` calls in try/catch (shelling out can fail)
- Uses `client.app.log()` for structured logging (not `console.log`)
- Debounces rapid events (200ms for tools, 2s for messages)
- Logs fire-and-forget `sciontool hook` errors to the OpenCode app logger for visibility

### Risk 2: Performance Overhead

Shelling out to `sciontool hook` for every tool event adds subprocess spawn overhead. The plugin uses:
- Debouncing (200ms window for tools)
- Fire-and-forget: `sciontool hook` calls are not awaited
- Temp file approach avoids shell quoting issues with here-strings

### Risk 3: Event Granularity

OpenCode's plugin events don't map 1:1 to Scion's hook events:
- No `model-start`/`model-end` from LLM calls — only inferred from `message.updated` (assistant)
- `message.updated` fires per-chunk (streaming), debounced at 2s to reduce noise
- No `agent-end` event — turn counting is still broken, but assistant text forwarding is now handled via `session-end` with collected assistant text

### Risk 4: Plugin Loading — RESOLVED

OpenCode auto-discovers `.js` files in `~/.config/opencode/plugins/`. Verified in running agent: plugin shows as "1 Plugin: scion-plugin" in OpenCode UI. No config reference needed in `opencode.json`.

### Risk 5: Auth Token Management

The plugin uses `sciontool hook` which handles auth internally. No direct Hub API calls are made, so token management is delegated to `sciontool`. If `sciontool` is unavailable, there is no fallback.

## Recent Fixes

### Bug: Dialect test used wrong heartbeat flag name
`opencode_test.go` used `_heartbeat` instead of `_scion_heartbeat`. The limits handler checks `_scion_heartbeat`, so the test was testing the wrong flag. Fixed to use `_scion_heartbeat` to match the plugin and limits handler.

## Next Steps

### P0: Fix Turn Counting (Gap 4)
**Impact:** `max_turns` limits have zero effect on OpenCode agents. An agent can run indefinitely without hitting its turn limit.
**Effort:** Medium (50-100 lines in either plugin or LimitsHandler).
**See details below.**

### P1: Improve Dialect Test Coverage
**Impact:** Low risk, defensive quality. Two code paths in the dialect parser have no test coverage.
**Effort:** Small (~20 lines).
**See details below.**

### P2: Document `container_hub_endpoint` for macOS
**Impact:** Medium — causes repeated heartbeat errors for macOS developers.
**Effort:** Trivial (1-2 paragraph addition to localhost workflows doc).

---

## Open Questions

### P0: Fix Turn Counting (Gap 4)

1. **Should LimitsHandler also process `session-end`?**
    - Treating `session-end` as a turn completion would enable `max_turns` for OpenCode
    - Model call counting now works correctly (heartbeat events are skipped via `_scion_heartbeat`)
    - **Recommendation:** Option A — Add `session-end` → turn increment in LimitsHandler

2. **Should we add `agent-end` to the plugin instead?**
    - Would fix turn counting (the remaining Gap 4 issue) via Option B above
    - Assistant text forwarding is now handled via `session-end` with collected assistant text (Gap 5 resolved)
    - **Recommendation:** Option B — Add `agent-end` on `session.deleted` with collected assistant text for turn counting parity

### P1: Improve Test Coverage

3. **Should we add `_activity` control event test to dialect tests?**
    - `opencode_test.go` has no test for the `_activity` control event or the `"hook_event_name"` fallback path
    - **Recommendation:** Add tests in `opencode_test.go`

### P2: Documentation & Resilience

4. **Should we document the `container_hub_endpoint` setting for macOS?**
    - `server.broker.container_hub_endpoint` in `~/.scion/settings.yaml` defaults to `http://172.17.0.1:9810` (Linux bridge)
    - macOS needs `http://host.docker.internal:9810`
    - **Recommendation:** Add to localhost workflows doc

5. **Should we implement Hub API direct fallback (Phase 5)?**
    - Currently the plugin is a single path: if `sciontool` is unavailable, events are lost
    - Plugin errors are now logged to the OpenCode app logger for visibility
    - Direct Hub API calls would provide resilience
    - **Recommendation:** Low priority; `sciontool` is guaranteed in Scion containers

6. **Should we add a native hook emission mode to OpenCode?**
    - Adding `--scion-hooks` flag to OpenCode that pipes events to stdout in `sciontool hook` JSON format
    - Would eliminate the plugin and `$` shelling overhead
    - Requires changes to OpenCode core (may not be feasible if OpenCode is a dependency, not a fork)
    - **Recommendation:** Defer; the plugin approach works for now

## Appendix: Reference — Claude Dialect Normalization

For comparison, here's how Claude's hook events map to normalized events (the pattern the OpenCode dialect follows, but simpler since the plugin emits pre-normalized events):

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
