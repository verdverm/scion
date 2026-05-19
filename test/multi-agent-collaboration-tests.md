## Multi-Agent Collaboration Tests

### Purpose

Test that scion can orchestrate multiple agents that communicate via `scion message`. The goal is to verify:

1. An orchestrator agent can start a child agent, send it a task, wait for the result, and produce an output file
2. The child agent can receive a task (via start message or post-start message), perform analysis, and send results back
3. The messaging system works end-to-end: send → deliver → receive → respond → receive
4. The `blocked` status + harness notification correctly resumes the orchestrator when a message arrives


Run these tests sequentially, trying different approaches for how the task reaches the child agent. Report results to the user after each approach — do NOT write results into this document.

**Report results as a message to the user.** You are running tests on behalf of a user who wants to know the summary of what happened, and if any failed just the overview. Do not explore why, do not try to work around system failures. Your only job is to run the tests and report the results.

### Critical Test Integrity Rules (Read Before Every Test)

These rules apply to YOU, the test runner. Violating them invalidates the test.

1. **NEVER send messages to agents directly during tests.** The entire purpose of these tests is to verify that the orchestrator agent can communicate with child agents via `scion message`. If you manually send messages to agents, you invalidate the test — you are not testing the messaging system, you are bypassing it. If an agent fails to send a message (e.g., because `scion` CLI is unavailable inside the container), that is a test failure to record, not a problem to work around by sending the message yourself.
2. **ALWAYS view FULL agent output — NEVER truncate with head/tail/grep/filters.** When inspecting agent terminal output via `scion look`, you MUST use `--full --plain` flags: `scion look <agent> --full --plain`. The `--full` flag captures the complete scrollback history (not just the current screen buffer), and `--plain` strips ANSI escape sequences so output is readable and parseable. When inspecting logs via `scion logs`, you MUST read the complete output. **Do NOT use `head`, `tail`, `grep`, `wc`, `sed`, `awk`, or any other command that filters, truncates, counts, or transforms the output.** You must read the raw, unfiltered output in full. This is not optional — truncating agent output means you miss critical actions (like whether an agent executed `scion message`), which invalidates the entire test. 
3. **NEVER create files inside agent containers.**  You corrupt the testing environment through manual modifications.
4. **NEVER modify agent state directly.** All communication must go through the Hub API's message system.
5. **BE PATIENT.** LLM agents take time to process, they may make mistakes and recover on their own. After starting an agent, wait at least 1-2 minutes before first check. After an agent sets `blocked`, wait at least 5 minutes before considering it stuck. After a child agent completes, wait at least 5 minutes before reporting a failure if the orchestrator hasn't been notified.
6. **Note failures, don't work around them.** If the code-reviewer LLM doesn't execute `scion message`, that is a RESULT — not a bug to fix by sending the message yourself. Remember what happened and move to the next approach. Do not investigate test or system failures.
8. **Only check agent status via the CLI or Hub API** Use `scion list`, `scion look`, and similar. Do not use `docker exec` to inspect agent state during tests — that is for debugging after a test has been run as failed.
9. **Wait between steps.** Do not chain checks back-to-back. Each monitoring step should be spaced by at least 1-2 minutes. Do not rush to the next check. Give the agents time to work and figure out any issues they hit themselves.

### Failure Protocol

When an agent fails to perform an expected action:

1. Note the timestamp of the last known agent activity.
2. Note what the agent DID do (e.g., "code-reviewer reached `completed` phase but no `scion message` was sent").
3. Check logs (`scion logs <agent>`) for evidence the agent attempted or skipped the action.
4. Report the failure to the user with all details above. Do NOT write results into this document.
5. Clean up agents with `scion delete`.
6. Move to the next approach — do NOT retry the same approach until you have analyzed why it failed.
7. After all approaches are tested, summarize findings across all approaches in your report to the user.

### Known Issues to Work Around

- **Messaging race condition**: If an agent starts another agent and immediately sends it a message, the message may be lost because the target agent isn't ready yet. The orchestrator must wait for the target agent to reach `running` phase AND perform an extra readiness check (e.g., verify `scion look <agent> --full --plain` shows the agent is at its prompt, not still initializing) before sending the message. A `running` phase only means the container is up — the agent may still be booting its harness and not yet listening for messages.
- **Orchestrator shortcutting**: The orchestrator agent tends to skip waiting steps and proceed to output steps before receiving messages. Always set `blocked` status after sending the child agent's task — the harness will auto-notify when a message arrives. Do NOT have the orchestrator poll for messages.
- **Blocked state must end the turn immediately**: When an agent sets its status to `blocked`, it must **immediately end its turn and do no further work**. Agents tend to interpret "wait" as "watch yourself" — continuing to execute steps after setting blocked. The `blocked` status is the agent's signal that it is now idle, waiting for a message to arrive via harness notification. Setting blocked and then continuing to work defeats the purpose: the agent may produce output before the message arrives, or miss the notification entirely. Instruction pattern: after the `sciontool status blocked` command, the agent's **only** next action should be to wait for the harness notification.

### Cleanup

Always use `scion delete`, not `scion stop` — stop leaves stale state.


### Code Review Test — Approach 1: Task in Post-Start Message

Orchestrator starts code-reviewer with no task, waits for it to be ready, then sends the task via `scion message`.

**Orchestrator task** (pass inline via command line):
```
You are the orchestrator. Coordinate with a code-reviewer agent to analyze three function specifications.

1. Start a code-reviewer agent (no task, just boot it): `scion start code-reviewer -y -t default --harness opencode --harness-auth none`
2. Wait until code-reviewer's phase is `running` (poll with `scion list --format json`), THEN verify it is actually ready by running `scion look code-reviewer --full --plain` and confirming it shows the agent prompt (not still initializing/booting), then send it this task:

   Analyze these three function specifications and provide a detailed review:

   FUNCTION 1: calculateSum(numbers)
   - Description: Sums all numbers in a list
   - Input: array of integers
   - Output: integer sum
   - Edge cases: empty list returns 0, negative numbers allowed

   FUNCTION 2: findMax(numbers)
   - Description: Finds the maximum value in a list
   - Input: array of integers
   - Output: maximum integer
   - Edge cases: empty list returns null, single element returns itself

   FUNCTION 3: reverseString(s)
   - Description: Reverses a string
   - Input: string
   - Output: reversed string
   - Edge cases: empty string returns empty, single char returns itself

   Provide:
   (1) Time and space complexity for each (O notation)
   (2) Whether each edge case is handled correctly and if any are missing
   (3) One actionable improvement suggestion per function

  After completing your analysis, you MUST send your report back to me using the scion CLI. Run this exact command:
     scion message orchestrator "YOUR COMPLETE REPORT HERE WITH ALL THREE SECTIONS"
     - This is mandatory. Do not just print your report in your response. You must execute the `scion message` command to deliver it through the Hub.
     - The command sends the message to the orchestrator agent by name. Use the exact format above with your full report as the message body. Send only a single message.
   After sending the report, mark your task as complete:
     sciontool status task_completed "Multi-agent code review complete"
   - **CRITICAL: Immediately end your turn after setting complete. Do NOT execute any further steps, do NOT continue working.** Setting complete is your signal that you are done.

 3. Set blocked status: `sciontool status blocked "Waiting for code-reviewer analysis"`
   - **CRITICAL: Immediately end your turn after setting blocked. Do NOT execute any further steps, do NOT poll for messages, do NOT continue working.** The blocked status is your signal that you are idle and waiting for a harness notification. Any work you do after setting blocked is wasted — you will not see the notification until your next turn.
4. When a message arrives, the harness will notify you and resume your turn.
5. Once you receive the report, create `/workspace/review-summary.md` containing:
   - A header section
   - code-reviewer's full report (copy their findings)
   - Your own brief assessment section at the end evaluating whether the functions are production-ready
6. Mark complete: `sciontool status task_completed "Multi-agent code review complete"`
```

**Start command**:
```bash
TASK=$(echo '...' | sed 's/"/\\"/g')
scion start orchestrator -y -t default --harness opencode --harness-auth none "$TASK"
```

**Monitor and validate**:
- code-reviewer starts and reaches `running` phase
- Orchestrator waits for code-reviewer to be ready before sending the message
- code-reviewer receives the task, performs analysis, and sends `scion message orchestrator "..."`
- Orchestrator receives the message and proceeds past blocked status
- `/workspace/review-summary.md` is created with the expected content (header, report, assessment)
- Orchestrator reaches `task_completed` phase

---

### Code Review Test — Approach 2: Task in code-reviewer's Start Message

Orchestrator starts code-reviewer with the full task embedded in its initial `scion start` message. This avoids the race condition entirely since the task is delivered during boot.

**Orchestrator task** (pass inline via command line):
```
You are the orchestrator. Coordinate with a code-reviewer agent to analyze three function specifications.

1. Start a code-reviewer agent with the full task embedded in its initial message:
   `scion start code-reviewer -y -t default --harness opencode --harness-auth none "Analyze these three function specifications and provide a detailed review:

   FUNCTION 1: calculateSum(numbers)
   - Description: Sums all numbers in a list
   - Input: array of integers
   - Output: integer sum
   - Edge cases: empty list returns 0, negative numbers allowed

   FUNCTION 2: findMax(numbers)
   - Description: Finds the maximum value in a list
   - Input: array of integers
   - Output: maximum integer
   - Edge cases: empty list returns null, single element returns itself

   FUNCTION 3: reverseString(s)
   - Description: Reverses a string
   - Input: string
   - Output: reversed string
   - Edge cases: empty string returns empty, single char returns itself

   Provide:
   (1) Time and space complexity for each (O notation)
   (2) Whether each edge case is handled correctly and if any are missing
   (3) One actionable improvement suggestion per function

  After completing your analysis, you MUST send your report back to me using the scion CLI. Run this exact command:
     scion message orchestrator \"YOUR COMPLETE REPORT HERE WITH ALL THREE SECTIONS\"
     - This is mandatory. Do not just print your report in your response. You must execute the `scion message` command to deliver it through the Hub.
     - The command sends the message to the orchestrator agent by name. Use the exact format above with your full report as the message body. Send only a single message.
   After sending the report, mark your task as complete:
     sciontool status task_completed "Multi-agent code review complete\""
   - **CRITICAL: Immediately end your turn after setting complete. Do NOT execute any further steps, do NOT continue working.** Setting complete is your signal that you are done.

2. Set blocked status: `sciontool status blocked "Waiting for code-reviewer analysis"`
   - **CRITICAL: Immediately end your turn after setting blocked. Do NOT execute any further steps, do NOT poll for messages, do NOT continue working.** The blocked status is your signal that you are idle and waiting for a harness notification. Any work you do after setting blocked is wasted — you will not see the notification until your next turn.
3. When a message arrives, the harness will notify you and resume your turn.
4. Once you receive the report, create `/workspace/review-summary.md` containing:
   - A header section
   - code-reviewer's full report (copy their findings)
   - Your own brief assessment section at the end evaluating whether the functions are production-ready
5. Mark complete: `sciontool status task_completed "Multi-agent code review complete"`
```

**Start command**:
```bash
TASK=$(echo '...' | sed 's/"/\\"/g')
scion start orchestrator -y -t default --harness opencode --harness-auth none "$TASK"
```

**Monitor and validate**:
- Same checks as Approach 1, but code-reviewer should begin analysis immediately upon boot (no post-start message required)
- Verify the task content is preserved intact when embedded in the `scion start` message (no truncation or mangling)

---

### Code Review Test — Approach 3: Immediate Message After Start (Race Condition)

Orchestrator starts code-reviewer with no task, then **immediately** sends the task via `scion message` without any readiness check. This tests the known messaging race condition — the message may be lost because code-reviewer isn't ready yet. The orchestrator agent may self-recover by polling for the message or retrying.

**Expectation:** This approach is expected to fail initially — the message will likely be lost because code-reviewer hasn't finished booting. However, the orchestrator may self-recover if it has logic to detect the missing message and retry. Watch for self-recovery behavior over a longer observation window.

**Orchestrator task** (pass inline via command line):
```
You are the orchestrator. Coordinate with a code-reviewer agent to analyze three function specifications.

1. Start a code-reviewer agent (no task, just boot it): scion start code-reviewer -y -t default --harness opencode --harness-auth none
2. Immediately send it this task (do NOT wait for readiness):

   Analyze these three function specifications and provide a detailed review:

   FUNCTION 1: calculateSum(numbers)
   - Description: Sums all numbers in a list
   - Input: array of integers
   - Output: integer sum
   - Edge cases: empty list returns 0, negative numbers allowed

   FUNCTION 2: findMax(numbers)
   - Description: Finds the maximum value in a list
   - Input: array of integers
   - Output: maximum integer
   - Edge cases: empty list returns null, single element returns itself

   FUNCTION 3: reverseString(s)
   - Description: Reverses a string
   - Input: string
   - Output: reversed string
   - Edge cases: empty string returns empty, single char returns itself

   Provide:
   (1) Time and space complexity for each (O notation)
   (2) Whether each edge case is handled correctly and if any are missing
   (3) One actionable improvement suggestion per function

   Use this exact command:
      scion message code-reviewer "YOUR FULL REPORT HERE WITH ALL THREE SECTIONS"

3. Set blocked status: sciontool status blocked "Waiting for code-reviewer analysis"
   - CRITICAL: Immediately end your turn after setting blocked. Do NOT execute any further steps, do NOT poll for messages, do NOT continue working.

4. When a message arrives, the harness will notify you and resume your turn.
5. Once you receive the report, create /workspace/review-summary.md containing:
   - A header section
   - code-reviewer's full report (copy their findings)
   - Your own brief assessment section at the end evaluating whether the functions are production-ready
6. Mark complete: sciontool status task_completed "Multi-agent code review complete"
```

**Start command**:
```bash
scion start orchestrator -y -t default --harness opencode --harness-auth none "$TASK"
```

**Monitor and validate**:
- code-reviewer starts and reaches `running` phase
- Orchestrator sends `scion message code-reviewer` immediately (no readiness check)
- **Expected failure:** The message may be lost because code-reviewer is still booting
- **Self-recovery check:** Does the orchestrator detect the missing message and retry? Does code-reviewer eventually receive a message (from a retry)?
- If no self-recovery: code-reviewer will sit idle (no task), orchestrator stays blocked forever
- If self-recovery: orchestrator notices code-reviewer is idle or no response, retries the message
- `/workspace/review-summary.md` should contain the expected content if recovery succeeds
- Both agents should reach `task_completed` phase

**Timing:** Wait at least 10-15 minutes before reporting failure. The orchestrator may take time to self-recover.
