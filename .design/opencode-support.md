# OpenCode Plugin Support for Scion

## Problem Statement

OpenCode is used as a harness in Scion (via `pkg/harness/opencode.go`), but there is **no bridge** between OpenCode's plugin event system and Scion's status/event infrastructure. When an agent runs OpenCode inside a Scion container, the Scion Hub has no visibility into what OpenCode is doing. This breaks the core Scion observability model.

Specifically:
- The `sciontool hook` system expects normalized events (`tool-start`, `tool-end`, `model-start`, `agent-start`, etc.) from harnesses like Claude Code and Gemini CLI
- OpenCode has no built-in hook dialect — it is not configured as one in `pkg/sciontool/hooks/dialects/`
- OpenCode's plugin system (TypeScript hooks in the OpenCode process) fires events like `tool.execute.before`, `session.status`, `message.updated` — but these never reach Scion's `agent-info.json` or Hub API
- The OpenCode harness in Scion declares `limits.max_turns: no` and `limits.max_model_calls: no` because "this harness has no hook dialect for turn events"
- The OpenCode harness declares `telemetry.native_emitter: no` because "native telemetry forwarding is not wired"

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

### Gap 4: No Limits Tracking — PARTIALLY RESOLVED
The dialect and event pipeline support limits, but OpenCode's plugin never emits `agent-end` or `model-end` events (only `session-end`). The `LimitsHandler` only increments on `agent-end` (turns) and `model-end` (model calls). **Turn counting and model-call counting do not work for OpenCode agents.** See Known Gaps below.

### Gap 5: No Assistant Text Forwarding — NOT RESOLVED
The HubHandler only forwards assistant text on `agent-end` events. OpenCode's plugin sends `session-end` on `session.deleted`/`session.error`, which the HubHandler does not treat as an assistant-text carrier. **Assistant responses never appear in the Messages tab.**

### Gap 6: No Permission-to-Input Bridge — RESOLVED
`permission.asked` → `notification` → `waiting_for_input` (sticky) is fully wired. `permission.replied` is logged and the next `tool-start` clears `waiting_for_input` via the StatusHandler's sticky-clearing logic.

## Status

### What's Implemented and Working

| Component | Status | File |
|---|---|---|
| **scion-plugin.js** | Done | `pkg/harness/opencode/embeds/scion-plugin.js` — Full event bridge. Debouncing (200ms tools, 2s messages), heartbeat (45s), sticky activity awareness, 20+ event handlers. |
| **OpenCode dialect** | Done | `pkg/sciontool/hooks/dialects/opencode.go` — Parses nested + flat formats. Registered in `registry.go`. |
| **Hook command** | Done | `--dialect=opencode` accepted in `hook.go` help text. |
| **Provision script** | Done | `pkg/harness/opencode/embeds/provision.py` — Handles auth (api-key, auth-file, vertex-ai, none), MCP server translation, plugin injection (`_inject_scion_plugin`). |
| **Container-script harness** | Done | `config.yaml` declares `provisioner.type: container-script`. Parity tests pass. |
| **Capabilities** | Done | `max_turns: yes`, `max_model_calls: yes`, `native_emitter: yes`, `vertex_ai: yes`, `none: yes`, MCP stdio/sse/streamable-http: yes. |
| **Dialect tests** | Done | `opencode_test.go` — Flat + nested formats, heartbeat, tool success/error. |
| **Parity tests** | Done | `opencode_parity_test.go` — Embed seeding, provision staging, script integration (happy path, MCP, no-creds). |

### Known Gaps

1. **Assistant text forwarding broken (Gap 5)** — The plugin sends `session-end` on `session.deleted`/`session.error`, but the HubHandler only forwards assistant text on `agent-end` events. No `agent-end` equivalent exists in OpenCode's plugin system. **Assistant responses never appear in the Messages tab.**

2. **Turn counting doesn't work (Gap 4 partial)** — The `LimitsHandler` only increments on `agent-end` (turns) and `model-end` (model calls). The OpenCode plugin never emits these events; it sends `session-end` instead. **`max_turns` and `max_model_calls` limits are ineffective for OpenCode agents.**

3. **Plugin loading unverified** — The plugin is deployed to `~/.config/opencode/plugins/scion-plugin.js` but there is no reference in `opencode.json` to tell OpenCode to load it. Whether OpenCode auto-discovers `.js` files in a `plugins/` directory is unknown — this is an untested assumption.

4. **No test for plugin seeding** — `TestOpenCodeEmbedsSeedRootSupportFiles` checks for `provision.py` and `opencode.json` but **doesn't verify the plugin file is seeded** to `home/.config/opencode/scion-plugin.js`. If the seeding logic changes, plugin deployment could silently break.

5. **`_activity` event is dead weight** — The plugin sends `_activity` events for heartbeats (line 128 of scion-plugin.js), and the dialect normalizes `_activity` → `""` (empty name). The StatusHandler's `eventToPhaseActivity` returns `nil` for empty names, so the event is silently dropped. The actual heartbeats are the `model-start` + `model-end` pairs sent separately (lines 182-183).

6. **No Hub API direct fallback (Phase 5 never implemented)** — The original design proposed dual-path: shell to `sciontool hook` + direct Hub API calls as fallback. Only the `sciontool hook` path exists. If `sciontool` is unavailable in the container, the plugin silently fails.

7. **No `agent-end` event ever emitted** — The canonical event for turn counting, assistant text forwarding, and post-agent cleanup. OpenCode uses `session.deleted` → `session-end` instead.

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
| Model call counting (max_model_calls) | **No** | LimitsHandler needs model-end from LLM calls, plugin has no equivalent |
| Assistant text → Messages tab | **No** | HubHandler needs agent-end with assistant_text |
| Session completion reporting | Yes | session.idle → response-complete → completed |
| Session error reporting | Yes | session.error → session-end → stopped |
| Telemetry (OTel spans) | Yes | TelemetryHandler processes events |
| Logging (agent.log) | Yes | LoggingHandler writes all events |
| Prompt capture (prompt.md) | Yes | PromptHandler saves user prompts |

## Gap Analysis (Open / Deferred)

### Gap 4: Limits Tracking — Needs `agent-end` or `session-end` handling in LimitsHandler

The `LimitsHandler` in `pkg/sciontool/hooks/handlers/limits.go` only processes `agent-end` (turns) and `model-end` (model calls). To support OpenCode:

**Option A:** Extend LimitsHandler to also process `session-end` as a turn completion event. This would count each OpenCode session as one turn.

**Option B:** Have the plugin infer model calls from message events (e.g., each assistant message chunk → model-end). This is fragile since OpenCode's message events don't carry LLM call boundaries.

**Option C:** Add a `model-end` event to the plugin that fires when the assistant finishes responding. This requires correlating `message.updated` events to infer LLM call boundaries.

### Gap 5: Assistant Text Forwarding — Needs `agent-end` equivalent

The HubHandler in `pkg/sciontool/hooks/handlers/hub.go` forwards assistant text only on `agent-end` events (line 132). Options:

**Option A:** Add an `agent-end` event to the plugin, emitted on `session.deleted`. The plugin would need to collect the assistant's final response text from message events.

**Option B:** Extend the HubHandler to also check `session-end` for assistant text (requires the plugin to carry it in the event data).

**Option C:** Use `session.idle` → `response-complete` to carry a task summary from the plugin's local state.

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
| `session.deleted` | `session-end` | `stopped` (phase) | Stops agent |
| `session.idle` | `response-complete` | `completed` | Session finished |
| `session.error` | `session-end` | `stopped` (phase) | With error detail |
| `tool.execute.before` | `tool-start` | `executing` | With tool name |
| `tool.execute.after` | `tool-end` | `working` | With success/error |
| `message.updated` (user) | `prompt-submit` | `thinking` | With prompt text (debounced 2s) |
| `message.updated` (assistant) | `model-start` | `thinking` | With content preview (debounced 2s) |
| `permission.asked` | `notification` | `waiting_for_input` | **Sticky** |
| `permission.replied` | (none) | — | Logged only, next tool-start clears sticky |
| `tui.command.execute` | `prompt-submit` | `thinking` | TUI command input |
| Heartbeat (timer) | `model-start` + `model-end` | `thinking` → `working` | Every 45s, suppressed in sticky states |

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

## Risks and Tradeoffs

### Risk 1: Plugin Reliability

OpenCode's plugin system may have lifecycle issues (e.g., plugins not loaded, errors silently swallowed). The plugin is defensive:
- Checks `SCION_AGENT_ID` before doing anything (graceful no-op outside Scion)
- Wraps all `$` calls in try/catch (shelling out can fail)
- Uses `client.app.log()` for structured logging (not `console.log`)
- Debounces rapid events (200ms for tools, 2s for messages)

### Risk 2: Performance Overhead

Shelling out to `sciontool hook` for every tool event adds subprocess spawn overhead. The plugin uses:
- Debouncing (200ms window for tools)
- Fire-and-forget: `sciontool hook` calls are not awaited
- Temp file approach avoids shell quoting issues with here-strings

### Risk 3: Event Granularity

OpenCode's plugin events don't map 1:1 to Scion's hook events:
- No `model-start`/`model-end` from LLM calls — only inferred from `message.updated` (assistant)
- `message.updated` fires per-chunk (streaming), debounced at 2s to reduce noise
- No `agent-end` event — means turn counting and assistant text forwarding are broken

### Risk 4: Plugin Loading Uncertainty

There is no verified mechanism for OpenCode to discover and load `scion-plugin.js` from `~/.config/opencode/plugins/`. The plugin may be silently ignored if OpenCode doesn't support auto-discovery of `.js` files in a `plugins/` directory. This is the single biggest risk — if the plugin doesn't load, the entire observability bridge is silent.

### Risk 5: Auth Token Management

The plugin uses `sciontool hook` which handles auth internally. No direct Hub API calls are made, so token management is delegated to `sciontool`. If `sciontool` is unavailable, there is no fallback.

## Open Questions

1. **Is the plugin actually loaded by OpenCode?**
   - Need to verify OpenCode discovers `~/.config/opencode/plugins/*.js` files
   - If not, we need to add a plugin reference to `opencode.json` or use OpenCode's npm plugin mechanism
   - **Action:** Test with a real agent to confirm plugin fires

2. **Should LimitsHandler also process `session-end`?**
   - Treating `session-end` as a turn completion would enable `max_turns` for OpenCode
   - Model call counting remains harder since OpenCode doesn't expose LLM call boundaries
   - **Recommendation:** Add `session-end` → turn increment in LimitsHandler

3. **Should we add `agent-end` to the plugin?**
   - Would fix both turn counting and assistant text forwarding
   - Requires the plugin to track the assistant's final response text from `message.updated` events
   - **Recommendation:** Add `agent-end` on `session.deleted` with collected assistant text

4. **Should we add a native hook emission mode to OpenCode?**
   - Adding `--scion-hooks` flag to OpenCode that pipes events to stdout in `sciontool hook` JSON format
   - Would eliminate the plugin and `$` shelling overhead
   - Requires changes to OpenCode core (may not be feasible if OpenCode is a dependency, not a fork)
   - **Recommendation:** Defer; the plugin approach works for now

5. **Should we implement Hub API direct fallback (Phase 5)?**
   - Currently the plugin is a single path: if `sciontool` is unavailable, events are lost
   - Direct Hub API calls would provide resilience
   - **Recommendation:** Low priority; `sciontool` is guaranteed in Scion containers

6. **Should we add a test for plugin seeding?**
   - `TestOpenCodeEmbedsSeedRootSupportFiles` doesn't verify `scion-plugin.js` lands in the harness-config tree
   - **Recommendation:** Add assertion in the existing test

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
