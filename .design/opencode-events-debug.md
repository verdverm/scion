# OpenCode Events Debug — Investigation Notes

## Event Flow Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│ Container (PID 1 = sciontool init)                                │
│                                                                   │
│  OpenCode process:                                               │
│    scion-plugin.js (TypeScript)                                   │
│      ├── Listens to 20+ OpenCode plugin events                   │
│      ├── Debounces tools (200ms), messages (2s)                  │
│      ├── Heartbeat every 45s (model-start + model-end)           │
│      └── Calls: sciontool hook --dialect=opencode (via temp file)│
│                                                                   │
│  sciontool hook --dialect=opencode:                               │
│    ├── OpenCodeDialect.Parse() → normalized hooks.Event          │
│    └── HarnessProcessor.dispatchEvent() → sequential handlers:   │
│         1. StatusHandler  → writes agent-info.json              │
│         2. LoggingHandler → writes agent.log                    │
│         3. PromptHandler  → saves prompt.md                     │
│         4. HubHandler     → POST /api/v1/agents/{id}/status     │
│         5. LimitsHandler  → increments turn/model counters      │
│         6. TelemetryHandler → OTel spans                        │
│                                                                   │
│  agent-info.json (HOME/agent-info.json):                         │
│    ├── Written by StatusHandler on every event                   │
│    ├── Read by: scion list, UI, HubHandler sticky checks         │
│    └── Read by: hubsync (syncs to Hub), runtime broker           │
│                                                                   │
│  Hub API (POST /api/v1/agents/{id}/status):                      │
│    ├── Writes to agent.Phase, agent.Activity, agent.Message      │
│    ├── Updates agent.LastSeen on every call                      │
│    ├── Publishes AgentStatus event for WebSocket push            │
│    └── Read by: UI, scion list (when hub-connected)              │
└──────────────────────────────────────────────────────────────────┘
```

## What Each Handler Does

### StatusHandler (`pkg/sciontool/hooks/handlers/status.go`)
- Maps each event name to a phase/activity pair via `eventToPhaseActivity()`
- Writes `agent-info.json` atomically (temp file + rename)
- Respects sticky activity semantics: `waiting_for_input`, `completed`, `blocked`, `limits_exceeded`, `crashed` resist being overwritten by non-new-work events
- `isNewWorkEvent()` returns true for: `prompt-submit`, `agent-start`, `session-start` — these clear sticky state unconditionally
- `tool-start` clears `waiting_for_input` specifically but preserves `completed`
- `notification` sets `waiting_for_input` directly (sticky)

### HubHandler (`pkg/sciontool/hooks/handlers/hub.go`)
- Reads `agent-info.json` via `readLocalActivity()` to check if current activity is sticky
- If sticky, most events are skipped (thinking/working don't overwrite sticky states)
- New work events (`session-start`, `prompt-submit`, `agent-start`) always send
- Sends `POST /api/v1/agents/{id}/status` with phase/activity/message
- Forwards assistant text from `agent-end` and `session-end` via `SendOutboundMessage`
- Has its own sticky check via `isLocalActivitySticky()` — reads the same file StatusHandler writes

### LimitsHandler (`pkg/sciontool/hooks/handlers/limits.go`)
- Only processes `agent-end` (turns) and `model-end` (model calls)
- Skips events with `_scion_heartbeat: true`
- Enforces `max_turns` and `max_model_calls` limits

## How Status Reaches the User

### `scion list`
1. Calls `agent.List()` which scans runtime containers + local agent directories
2. Reads `agent-info.json` from `home/agent-info.json` for latest container status
3. Also reads Hub API when hub-connected
4. Falls back to `scion-agent.json` / `scion-agent.yaml` if no agent-info.json

### Web UI
1. Fetches agent list from Hub API `GET /api/v1/projects/{id}/agents`
2. Each agent includes `phase`, `activity`, `message`, `lastSeen` from the database
3. Real-time updates via WebSocket `AgentStatus` events (published by Hub on status update)
4. For local/solo mode: reads from `agent-info.json` via hubsync

### The Connection Points
The Hub stores status on the `agents` table:
- `phase` — lifecycle phase (created, running, stopped, error)
- `activity` — runtime activity (working, thinking, executing, waiting_for_input, completed)
- `message` — last status message
- `last_seen` — timestamp of last heartbeat/status update

The HubHandler in sciontool hook calls `POST /api/v1/agents/{id}/status` which writes to this table. The Hub publishes an `AgentStatus` event to WebSocket subscribers.

## Known Issues Found

### 1. Plugin is fire-and-forget (no delivery guarantees)
In `scion-plugin.js`, `sendHook()` calls `sciontool hook` as a fire-and-forget subprocess:
```js
client.$`timeout ${HOOK_TIMEOUT_MS} sh -c 'cat $1 | sciontool hook --dialect=opencode 2>/dev/null; rm -f $1' _ ${tmpPath}`.catch(...)
```
- Errors are only logged to OpenCode's app logger, not surfaced
- `2>/dev/null` suppresses all stderr — any sciontool errors are invisible
- No retry logic
- If `sciontool` is missing or the subprocess hangs, events are silently lost

### 2. HubHandler reads agent-info.json synchronously
`readLocalActivity()` does a blocking `os.ReadFile()` on every event. This means:
- Every HubHandler call reads the file written by StatusHandler
- If StatusHandler hasn't written yet (race), HubHandler sees stale activity
- The file read happens inside a 5-second context timeout

### 3. Sticky activity blocks many events
The HubHandler's sticky check is aggressive. Once activity becomes `completed` or `waiting_for_input`, subsequent `model-start`, `tool-end`, `model-end` events are all skipped. This is correct for preventing status thrashing, but means:
- After a session goes idle (`session.idle` → `response-complete` → `completed`), the activity stays `completed`
- The next user prompt sends `prompt-submit` (new work event) which clears sticky
- But if OpenCode sends events in an unexpected order, they may be silently dropped

### 4. No visibility into whether events reach the Hub
- Plugin errors go to OpenCode's app logger only
- `sciontool hook` stderr is suppressed (`2>/dev/null`)
- HubHandler errors are logged at debug level only
- No way to verify events are actually reaching the Hub without checking the database directly

### 5. Heartbeat sends to wrong endpoint on macOS
`SCION_HUB_ENDPOINT` defaults to `http://localhost:9810` inside containers. On macOS, Docker containers can't reach `localhost` — needs `host.docker.internal`. This is configurable via `server.broker.container_hub_endpoint` in `~/.scion/settings.yaml` but not documented.

## Debugging Checklist

When an OpenCode agent runs but status doesn't update:

1. **Check if plugin is loaded** — OpenCode UI shows "1 Plugin: scion-plugin"
2. **Check plugin logs inside container** — `docker exec <container> cat /home/scion/.config/opencode/app.log` (or wherever OpenCode logs)
3. **Check agent-info.json** — `docker exec <container> cat /home/scion/agent-info.json`
4. **Check Hub API directly** — `curl -H "Authorization: Bearer <token>" <hub>/api/v1/agents/<id>` — verify phase/activity fields
5. **Check Hub database** — `sqlite3 <hub.db> "SELECT phase, activity, message, last_seen FROM agents WHERE id = '<id>'"`
6. **Check sciontool hook stderr** — Remove `2>/dev/null` from the plugin's sendHook call temporarily
7. **Check WebSocket** — Browser DevTools Network tab → WS frames → look for `AgentStatus` events

## File Reference

| File | Role |
|---|---|
| `pkg/harness/opencode/embeds/scion-plugin.js` | OpenCode plugin — event capture + subprocess spawning |
| `pkg/sciontool/hooks/dialects/opencode.go` | Parses plugin JSON into normalized Event |
| `pkg/sciontool/hooks/handlers/status.go` | Writes agent-info.json with phase/activity |
| `pkg/sciontool/hooks/handlers/hub.go` | POSTs to Hub API, reads agent-info.json for sticky check |
| `pkg/sciontool/hooks/handlers/limits.go` | Turn/model-call counting |
| `pkg/sciontool/hub/client.go` | Hub API client (UpdateStatus, SendOutboundMessage, Heartbeat) |
| `pkg/hub/handlers.go:2753` | Hub server handler for POST /api/v1/agents/{id}/status |
| `pkg/agent/list.go` | `scion list` — reads agent-info.json + Hub API |
| `pkg/hubsync/sync.go:1011` | Hub sync — reads agent-info.json for local agent info |

## Testable Hypotheses

### H1: Events are being sent but silently failing (plugin subprocess issue)

**Hypothesis:** The plugin fires events to `sciontool hook`, but the subprocess calls fail silently due to `2>/dev/null` suppression, missing `sciontool` binary, or timeout. Events never reach any handler.

**Verdict: NEEDS RUNTIME VERIFICATION**

**Test:**
```bash
# 1. Temporarily modify the plugin to NOT suppress stderr and NOT timeout:
#    Replace in scion-plugin.js line 110:
#    FROM: client.$`timeout ${HOOK_TIMEOUT_MS} sh -c 'cat $1 | sciontool hook --dialect=opencode 2>/dev/null; rm -f $1' _ ${tmpPath}`.catch(...)
#    TO:   client.$`sh -c 'cat $1 | sciontool hook --dialect=opencode; rm -f $1' _ ${tmpPath}`.catch(...)
#
# 2. Run an agent with a simple task (e.g., "echo hello")
# 3. Check OpenCode's app logger inside container:
#    docker exec <container> cat /home/scion/.config/opencode/app.log
#    OR check container logs: docker logs <container>
#
# 4. Expected if hypothesis is TRUE:
#    - app.log will show "scion hook failed" entries with actual error messages
#    - Container stderr will show sciontool output
#
# 5. Expected if hypothesis is FALSE:
#    - No error messages in logs
#    - Events are reaching handlers but something else is blocking visibility
```

**Alternative non-invasive test:**
```bash
# 1. Check if sciontool exists in the container:
docker exec <container> which sciontool
#
# 2. Check if the temp files are being created (proves plugin is firing):
docker exec <container> ls -la /tmp/scion-hook-*.json 2>/dev/null | wc -l
#
# 3. Manually feed a known event to sciontool hook inside the container:
echo '{"name":"tool-start","data":{"tool_name":"Bash","source":"opencode"}}' | docker exec -i <container> sciontool hook --dialect=opencode
#
# 4. Check if agent-info.json was updated:
docker exec <container> cat /home/scion/agent-info.json
```

**What would invalidate:** If `sciontool` exists, temp files are created, and manual pipe works — the plugin is delivering events correctly.

---

### H2: `completed` activity is sticky and blocks all subsequent status updates

**Hypothesis:** When OpenCode sends `session.idle`, the plugin fires `response-complete`. StatusHandler maps this to `completed` (sticky). HubHandler then checks `isLocalActivitySticky()`, sees `completed`, and silently drops ALL subsequent events (`model-start`, `tool-end`, etc.) until a new work event arrives. The agent appears stuck at "completed" even though it's actively working.

**Verdict: TRUE (confirmed by code inspection)**

Evidence:
- `scion-plugin.js:263-266`: `"session.idle"` → `sendHook("response-complete")`
- `status.go:299-300`: `EventResponseComplete` → `ActivityCompleted`
- `scion-plugin.js:45-51`: `completed` is in `STICKY_ACTIVITIES` set
- `hub.go:71-73`: `EventModelStart` blocked when `isLocalActivitySticky()` returns true → logs "Skipping thinking (local activity is sticky)"
- `hub.go:106-109`: `EventToolStart` blocked when activity is `completed` or `limits_exceeded` → logs "Skipping executing (completed is sticky, post-completion tool)"
- `hub.go:154-156`: `EventToolEnd/AgentEnd/ModelEnd` blocked when `isLocalActivitySticky()` → logs "Skipping working (local activity is sticky)"
- `status.go:190-194`: `updateActivityIfNotSticky()` returns nil when current activity is sticky (except `waiting_for_input` cleared by tool-start)
- The ONLY way to clear sticky state: `isNewWorkEvent()` → `prompt-submit`, `agent-start`, `session-start`

**Result:** After `session.idle` fires, the agent is locked at `completed`. All `model-start`, `tool-start`, `tool-end`, `model-end` events are silently dropped by BOTH StatusHandler and HubHandler. The agent only "wakes up" when a new user prompt arrives (→ `prompt-submit` → `thinking`).

This is the PRIMARY ROOT CAUSE of missing UI status updates.

---

### H3: StatusHandler writes agent-info.json but HubHandler never sends to Hub

**Hypothesis:** StatusHandler successfully writes `agent-info.json` (so `scion list` might show updates), but HubHandler's `POST /api/v1/agents/{id}/status` calls are failing due to network issues, auth problems, or the Hub endpoint being unreachable from inside the container. The UI (which reads from Hub) shows stale data while local status is fine.

**Verdict: NEEDS RUNTIME VERIFICATION**

**Test:**
```bash
# 1. Check if agent-info.json is being updated (StatusHandler is working):
docker exec <container> stat /home/scion/agent-info.json
# Note the mtime, then wait 30s, check again
docker exec <container> stat /home/scion/agent-info.json
#
# 2. If mtime changed, StatusHandler is writing. Now check Hub connectivity:
docker exec <container> curl -s -o /dev/null -w "%{http_code}" \
    http://host.docker.internal:9810/api/v1/agents/<agent-id>/status
#
# 3. Check if Hub API is reachable with auth:
docker exec <container> cat /home/scion/.scion/scion-token
# (copy the token, then test from host:)
curl -H "Authorization: Bearer <token>" \
    http://host.docker.internal:9810/api/v1/agents/<agent-id> | jq .
#
# 4. Check the actual agent record in Hub database:
sqlite3 ~/.scion/hub.db "SELECT phase, activity, message, last_seen FROM agents WHERE id = '<agent-id>';"
#
# Expected if TRUE:
#   - agent-info.json mtime is fresh (StatusHandler works)
#   - curl to Hub returns 200 (connectivity works)
#   - But database shows stale phase/activity (HubHandler calls failing)
```

---

### H4: Events are debounced and the agent completes before they fire

**Hypothesis:** The plugin debounces tool events at 200ms and message events at 2s. For fast-executing tasks, the agent may complete before the debounce timer fires, meaning only the final state (or nothing) reaches the handlers. The UI sees a jump from "running" directly to "completed" with no intermediate states.

**Verdict: TRUE (confirmed by code inspection) — but secondary to H2**

Evidence:
- `scion-plugin.js:39`: `DEBOUNCE_MS = 200` (tools), `MSG_DEBOUNCE_MS = 2000` (messages)
- `scion-plugin.js:159-165`: debounce clears previous timer, fires only after cooldown
- `scion-plugin.js:239`: `debounce(sendHook, 200)` — rapid tool events batch into single send
- `scion-plugin.js:240`: `debounce(sendHook, 2000)` — message events batch over 2s

**How it works in practice:**
```
Tool A fires at T=0ms     → timer starts, fires at T=200ms
Tool B fires at T=50ms    → timer CLEARED, new timer starts, fires at T=250ms
Tool C fires at T=100ms   → timer CLEARED, new timer starts, fires at T=300ms
                           → Only ONE event sent at T=300ms (last tool in batch)
```

**Impact:** For a task with multiple rapid tools, only the LAST tool in each 200ms window is reported. Intermediate tool executions are silently dropped. This is by design (prevents status thrashing) but means the UI shows incomplete event history.

**However:** This is secondary to H2. Even if all events were sent without debouncing, they would still be blocked by the sticky `completed` state.

---

### H5: The `_activity` control event from the plugin is dead code

**Hypothesis:** The plugin sends `_activity` events (via `sendActivity()` helper at line 136-138 of scion-plugin.js), but the OpenCode dialect's `normalizeEventName()` returns an empty string for `_activity`, and no handler processes empty-name events. The StatusHandler's `eventToPhaseActivity()` returns nil for empty event names, so the event is silently dropped. The plugin's heartbeat and activity tracking never actually update status.

**Verdict: TRUE (confirmed by code inspection)**

Evidence — three layers of dead code:

1. **Plugin never calls `sendActivity()`** (`scion-plugin.js:136-138`):
   - `sendActivity()` is defined but never invoked anywhere in the plugin
   - All event handlers call `sendHook()` directly, not `sendActivity()`
   - This function is dead code

2. **Dialect returns empty string** (`opencode.go:101-104`):
   ```go
   case "_activity":
       return ""  // empty string as signal to handlers
   ```

3. **StatusHandler silently drops empty-name events** (`status.go:53-55`):
   ```go
   result := eventToPhaseActivity(event)
   if result == nil { return nil }  // "" has no case, returns nil → dropped
   ```

4. **`eventToPhaseActivity()` has no case for `""`** (`status.go:272-314`):
   - Switch covers all named events but not empty string
   - Falls through to `default: return nil`

**Result:** `_activity` events are a completely dead path. They are parsed, normalized to `""`, and silently dropped by every handler. The `sendActivity()` helper is dead code that was likely intended for future use but never wired up.

---

### H6: HubHandler's sticky check races with StatusHandler

**Verdict: FALSE (confirmed by code inspection)**

Evidence:
- `pkg/sciontool/hooks/harness.go:98-110`: `dispatchEvent()` calls handlers sequentially in the same goroutine
- StatusHandler runs first (earlier in handler chain) → writes `agent-info.json` atomically
- HubHandler runs second → reads `agent-info.json` synchronously
- Since they're in the same goroutine, there's no concurrency — StatusHandler ALWAYS completes before HubHandler starts

No race condition exists. The handlers are sequential, not concurrent.

---

### H7: Agent state is "completed" but should be "running" with activity tracking

**Hypothesis:** When OpenCode finishes a task and goes idle (waiting for next user message), the plugin sends `session.idle` → `response-complete` → `completed`. This is a terminal sticky state. The agent appears "done" in the UI, but it's actually in a ready/waiting state. The lifecycle is: `running` → `completed` → (new prompt) → `running` → `completed` again. The UI shows "completed" most of the time because that's the terminal state after each subtask.

**Verdict: TRUE (confirmed by code inspection) — direct consequence of H2**

Evidence:
- `scion-plugin.js:263-266`: `"session.idle"` → `sendHook("response-complete")`
- `status.go:299-300`: `EventResponseComplete` → `ActivityCompleted`
- `hub.go:180-191`: HubHandler sends `ActivityCompleted` for `response-complete`

**Event lifecycle per task:**
```
user prompt → prompt-submit → thinking
assistant start → model-start → thinking
tool A → tool-start → executing → tool-end → working
tool B → tool-start → executing → tool-end → working
session.idle → response-complete → completed (STICKY, blocks all future events)
```

**Result:** The agent appears "completed" between every subtask. This is NOT a bug in the traditional sense — it's a design choice that `session.idle` means "task done." But it creates a poor UX because the agent appears dead between prompts.

**Proposed fix** (change `session.idle` mapping):
```js
// In scion-plugin.js line 263-266, change:
"session.idle": async () => {
  await sendHook(client, "response-complete", { source: "opencode" })
},

// To:
"session.idle": async () => {
  // Non-sticky "working" keeps agent visible as active/ready
  await sendHook(client, "tool-end", { tool_name: "Idle", source: "opencode" })
},
```

`tool-end` maps to `ActivityWorking` (non-sticky), so the agent stays visible as "working" instead of locking at "completed."

---

## Claude vs OpenCode — Side-by-Side Comparison

This is the critical lens. Claude harness works as expected. Here's why:

### Event Mapping

| Claude Hook | Claude Dialect → Normalized | OpenCode Plugin → Normalized |
|---|---|---|
| `SessionStart` | `session-start` | `session-start` ✅ |
| `SessionEnd` | `session-end` | `session-end` ✅ |
| `UserPromptSubmit` | `prompt-submit` | `prompt-submit` ✅ (via `message.updated` user) |
| `PreToolUse` | `tool-start` | `tool-start` ✅ |
| `PostToolUse` | `tool-end` | `tool-end` ✅ |
| `Stop` | `agent-end` | — (no equivalent) |
| `SubagentStop` | `subagent-end` | — |
| `Notification` | `notification` | `notification` ✅ |
| `BeforeModel` | `model-start` | `model-start` ✅ (via `message.updated` assistant) |
| `AfterModel` | `model-end` | — (no explicit equivalent) |
| — | — | `session.idle` → `response-complete` ❌ |

### The Critical Difference: `session.idle`

Claude has **no idle/completion event**. Its lifecycle per turn is:

```
UserPromptSubmit → prompt-submit → thinking
PreToolUse → tool-start → executing
PostToolUse → tool-end → working
( repeat tools )
Stop → agent-end → PhaseStopped (terminal phase)
```

When a new prompt arrives:
```
UserPromptSubmit → prompt-submit → thinking (Hub API accepts status update regardless of phase)
```

OpenCode has an **extra `session.idle` event** that Claude does not have:

```
session.idle → response-complete → ActivityCompleted (STICKY)
```

This `session.idle` fires when OpenCode finishes a task and waits for the next user message. It maps to `response-complete` which sets `ActivityCompleted` — a **sticky activity that blocks ALL subsequent events**.

### Why Claude Works

1. **No sticky `completed` state**: Claude's events cycle through `thinking → executing → working` without ever hitting the `completed` sticky state. The `Stop` → `agent-end` → `PhaseStopped` is a phase-level change, not an activity-level block.

2. **`Stop` is a phase transition, not an activity block**: `agent-end` sets `PhaseStopped`. When the next `prompt-submit` arrives, HubHandler sends `ActivityThinking` to the Hub API. The Hub API likely accepts the update regardless of current phase, so the agent transitions back to `PhaseRunning`.

3. **Synchronous hooks**: Claude's hooks are configured in `settings.json` and fire synchronously. Each hook call blocks until `sciontool hook` completes. No fire-and-forget, no `2>/dev/null`, no debounce.

4. **One `Stop` per turn**: Claude fires `Stop` exactly once when the agent finishes a turn. OpenCode fires `session.idle` every time it goes idle — potentially multiple times, each one re-setting the sticky `completed` state.

### Why OpenCode Fails

1. **`session.idle` → `response-complete` → `ActivityCompleted` (sticky)**: Once `session.idle` fires, the agent locks at `completed`. All `model-start`, `tool-start`, `tool-end`, `model-end` events are silently dropped by both StatusHandler and HubHandler.

2. **Fire-and-forget plugin**: Events are sent as detached subprocesses with `2>/dev/null`. Errors are invisible.

3. **Debounce**: Tool events batch over 200ms, message events over 2s. Multiple events collapse into one.

4. **No `agent-end` equivalent**: OpenCode's `session.deleted` maps to `session-end` (PhaseStopped), but `session.idle` (the per-turn completion signal) maps to `response-complete` instead of `agent-end`.

### The Fix

**Remove or change the `session.idle` mapping.** Two options:

**Option A: Remove `session.idle` entirely**
```js
// Remove or empty the handler:
"session.idle": async () => {
  // Don't send any event — let the agent stay in its last activity state
},
```
This matches Claude's behavior: no explicit completion event per turn. The agent stays in `working` after the last `tool-end` until the next `prompt-submit`.

**Option B: Map to `agent-end` instead of `response-complete`**
```js
"session.idle": async () => {
  await sendHook(client, "agent-end", { source: "opencode" })
},
```
This would set `PhaseStopped` (like Claude's `Stop`), then the next `prompt-submit` would wake it up. But this changes the phase lifecycle significantly.

**Option C (recommended): Map to `tool-end` with a descriptive tool name**
```js
"session.idle": async () => {
  await sendHook(client, "tool-end", { tool_name: "Idle", source: "opencode" })
},
```
`tool-end` → `ActivityWorking` (non-sticky). The agent stays visible as "working" instead of locking at "completed". Matches the "ready for next task" semantic.

**Option A is the closest match to Claude.** Claude doesn't fire any event when it finishes a turn — it just stops firing hooks. The agent stays in its last state (`working` after `PostToolUse`, `thinking` after `AfterModel`) until the next `UserPromptSubmit`. OpenCode should do the same: don't fire an event on `session.idle`, let the last activity persist.
