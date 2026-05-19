/*
Copyright 2025 The Scion Authors.
*/

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hooks"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hooks/dialects"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
)

// TestOpenCodeFullEventPipeline exercises the complete event flow:
// plugin JSON → dialect Parse → StatusHandler → LoggingHandler → HubHandler
// with a mock Hub server that records every request.
//
// This is the integration test that replaces speculation with evidence.
func TestOpenCodeFullEventPipeline(t *testing.T) {
	// -----------------------------------------------------------------------
	// 1. Set up mock Hub server that records EVERY request
	// -----------------------------------------------------------------------
	type hubCall struct {
		method  string
		path    string
		payload map[string]interface{}
	}

	var calls []hubCall
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)

		mu.Lock()
		calls = append(calls, hubCall{
			method:  r.Method,
			path:    r.URL.Path,
			payload: payload,
		})
		mu.Unlock()

		fmt.Fprintf(os.Stderr, "[MOCK-HUB] %s %s => %s (activity=%s phase=%s msg=%s)\n",
			r.Method, r.URL.Path,
			string(payload["status"].(string)),
			payload["activity"], payload["phase"], payload["message"])
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	// -----------------------------------------------------------------------
	// 2. Set up temp HOME with agent-info.json
	// -----------------------------------------------------------------------
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	// Start with empty agent-info.json
	os.WriteFile(tmpDir+"/agent-info.json", []byte(`{}`), 0644)

	// -----------------------------------------------------------------------
	// 3. Set up sciontool env
	// -----------------------------------------------------------------------
	os.Setenv("SCION_HUB_ENDPOINT", server.URL)
	os.Setenv("SCION_AUTH_TOKEN", "test-token")
	os.Setenv("SCION_AGENT_ID", "test-agent-123")
	defer func() {
		os.Unsetenv("SCION_HUB_ENDPOINT")
		os.Unsetenv("SCION_HUB_URL")
		os.Unsetenv("SCION_AUTH_TOKEN")
		os.Unsetenv("SCION_AGENT_ID")
	}()

	// -----------------------------------------------------------------------
	// 4. Enable debug logging
	// -----------------------------------------------------------------------
	log.SetDebug(true)
	log.SetLogPath(tmpDir + "/agent.log")

	// -----------------------------------------------------------------------
	// 5. Define test events (mimicking what scion-plugin.js sends)
	// -----------------------------------------------------------------------
	type testEvent struct {
		name       string
		data       map[string]interface{}
		expectCall bool
	}

	// This is the full OpenCode event lifecycle:
	events := []testEvent{
		{"session-start", map[string]interface{}{"source": "opencode"}, true},
		{"prompt-submit", map[string]interface{}{"prompt": "fix the bug", "source": "opencode"}, true},
		{"agent-start", map[string]interface{}{"source": "opencode"}, true},
		{"model-start", map[string]interface{}{"prompt": "Assistant response", "source": "opencode"}, true},
		{"tool-start", map[string]interface{}{"tool_name": "Bash", "source": "opencode"}, true},
		{"tool-end", map[string]interface{}{"tool_name": "Bash", "success": true, "source": "opencode"}, true},
		{"model-end", map[string]interface{}{"source": "opencode"}, true},
		{"agent-end", map[string]interface{}{"source": "opencode"}, true},
		{"session-end", map[string]interface{}{"source": "opencode", "assistant_text": "Done!"}, true},
	}

	// -----------------------------------------------------------------------
	// 6. Process each event through the full pipeline
	// -----------------------------------------------------------------------
	for i, evt := range events {
		t.Logf("\n=== EVENT %d: %s ===", i+1, evt.name)

		// Read current agent-info.json
		infoData, _ := os.ReadFile(tmpDir + "/agent-info.json")
		var info map[string]interface{}
		json.Unmarshal(infoData, &info)
		t.Logf("  [PRE] agent-info.json activity=%q phase=%q", info["activity"], info["phase"])

		// Parse as OpenCode dialect (what plugin sends)
		rawData := map[string]interface{}{
			"name": evt.name,
			"data": evt.data,
		}
		jsonData, _ := json.Marshal(rawData)

		// Create processor like sciontool hook does
		processor := hooks.NewHarnessProcessor()
		dialects.RegisterBuiltins(processor)

		// Create handlers like the real hook command does
		statusHandler := NewStatusHandler()
		loggingHandler := NewLoggingHandler()
		hubHandler := NewHubHandler()

		processor.AddHandler(statusHandler.Handle)
		processor.AddHandler(loggingHandler.Handle)
		if hubHandler != nil {
			processor.AddHandler(hubHandler.Handle)
		}

		// Process the event
		err := processor.ProcessRaw(rawData, "opencode")
		if err != nil {
			t.Logf("  [ERROR] processor error: %v", err)
		}
		_ = jsonData

		// Read updated agent-info.json
		infoData, _ = os.ReadFile(tmpDir + "/agent-info.json")
		json.Unmarshal(infoData, &info)
		t.Logf("  [POST] agent-info.json activity=%q phase=%q", info["activity"], info["phase"])

		// Check Hub calls
		mu.Lock()
		newCalls := len(calls)
		mu.Unlock()

		if evt.expectCall {
			t.Logf("  [HUB] expected call, got %d new calls", newCalls)
		} else {
			if newCalls > 0 {
				t.Logf("  [HUB] WARNING: unexpected call(s) received")
			} else {
				t.Logf("  [HUB] no call (as expected)")
			}
		}

		// Print the last call details
		if newCalls > 0 {
			mu.Lock()
			last := calls[len(calls)-1]
			mu.Unlock()
			t.Logf("  [HUB LAST] status=%s activity=%s phase=%s msg=%q",
				last.payload["status"], last.payload["activity"],
				last.payload["phase"], last.payload["message"])
		}
	}

	// -----------------------------------------------------------------------
	// 7. Print full Hub call summary
	// -----------------------------------------------------------------------
	t.Logf("\n=== HUB CALL SUMMARY (%d total) ===", len(calls))
	for i, call := range calls {
		t.Logf("  [%d] %-30s status=%-20s activity=%-20s phase=%-10s msg=%q",
			i+1, call.path,
			call.payload["status"],
			call.payload["activity"],
			call.payload["phase"],
			call.payload["message"])
	}
}

// TestOpenCodeStickyBreakdown tests the specific scenario where sticky state
// blocks events — simulating the exact OpenCode flow that breaks.
func TestOpenCodeStickyBreakdown(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	os.WriteFile(tmpDir+"/agent-info.json", []byte(`{}`), 0644)

	os.Setenv("SCION_HUB_ENDPOINT", "")
	os.Unsetenv("SCION_HUB_URL")
	os.Unsetenv("SCION_AUTH_TOKEN")
	os.Unsetenv("SCION_AGENT_ID")

	log.SetDebug(true)
	log.SetLogPath(tmpDir + "/agent.log")

	// Start with "completed" activity (what happens after agent finishes)
	os.WriteFile(tmpDir+"/agent-info.json", []byte(`{"activity":"completed","phase":"running"}`), 0644)

	// Now send a new prompt — should this clear sticky?
	t.Log("\n=== Sending prompt-submit when activity=completed ===")

	err := processor.ProcessRaw(map[string]interface{}{
		"name": "prompt-submit",
		"data": map[string]interface{}{
			"prompt": "new task",
			"source": "opencode",
		},
	}, "opencode")
	if err != nil {
		t.Logf("Error: %v", err)
	}

	infoData, _ := os.ReadFile(tmpDir + "/agent-info.json")
	var info map[string]interface{}
	json.Unmarshal(infoData, &info)
	t.Logf("After prompt-submit: activity=%q phase=%q", info["activity"], info["phase"])
}

// TestOpenCodeStickyWithHub simulates the full sticky scenario with Hub calls.
func TestOpenCodeStickyWithHub(t *testing.T) {
	var calls []hubCall
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)

		mu.Lock()
		calls = append(calls, hubCall{
			path:    r.URL.Path,
			payload: payload,
		})
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	// Start with "completed" — agent finished a turn
	os.WriteFile(tmpDir+"/agent-info.json", []byte(`{"activity":"completed","phase":"running"}`), 0644)

	os.Setenv("SCION_HUB_ENDPOINT", server.URL)
	os.Setenv("SCION_AUTH_TOKEN", "test-token")
	os.Setenv("SCION_AGENT_ID", "test-agent")
	defer func() {
		os.Unsetenv("SCION_HUB_ENDPOINT")
		os.Unsetenv("SCION_HUB_URL")
		os.Unsetenv("SCION_AUTH_TOKEN")
		os.Unsetenv("SCION_AGENT_ID")
	}()

	log.SetDebug(true)
	log.SetLogPath(tmpDir + "/agent.log")

	// Scenario: agent finishes turn (completed), then new prompt arrives
	t.Log("\n=== SCENARIO: completed → prompt-submit → agent-start → model-start ===")

	// 1. New prompt should clear sticky
	t.Log("\n--- Step 1: prompt-submit (should clear sticky) ---")
	processor := hooks.NewHarnessProcessor()
	dialects.RegisterBuiltins(processor)
	processor.AddHandler(NewStatusHandler().Handle)
	processor.AddHandler(NewLoggingHandler().Handle)
	processor.AddHandler(NewHubHandler().Handle)

	processor.ProcessRaw(map[string]interface{}{
		"name": "prompt-submit",
		"data": map[string]interface{}{"prompt": "fix it", "source": "opencode"},
	}, "opencode")

	mu.Lock()
	t.Logf("After prompt-submit: %d Hub calls", len(calls))
	if len(calls) > 0 {
		t.Logf("  Last call: status=%s activity=%s", calls[len(calls)-1].payload["status"], calls[len(calls)-1].payload["activity"])
	}
	mu.Unlock()

	// 2. agent-start (thinking)
	t.Log("\n--- Step 2: agent-start ---")
	calls = nil
	processor = hooks.NewHarnessProcessor()
	dialects.RegisterBuiltins(processor)
	processor.AddHandler(NewStatusHandler().Handle)
	processor.AddHandler(NewLoggingHandler().Handle)
	processor.AddHandler(NewHubHandler().Handle)

	processor.ProcessRaw(map[string]interface{}{
		"name": "agent-start",
		"data": map[string]interface{}{"source": "opencode"},
	}, "opencode")

	mu.Lock()
	t.Logf("After agent-start: %d Hub calls", len(calls))
	if len(calls) > 0 {
		t.Logf("  Last call: status=%s activity=%s", calls[len(calls)-1].payload["status"], calls[len(calls)-1].payload["activity"])
	}
	mu.Unlock()

	// 3. model-start (thinking)
	t.Log("\n--- Step 3: model-start ---")
	calls = nil
	processor = hooks.NewHarnessProcessor()
	dialects.RegisterBuiltins(processor)
	processor.AddHandler(NewStatusHandler().Handle)
	processor.AddHandler(NewLoggingHandler().Handle)
	processor.AddHandler(NewHubHandler().Handle)

	processor.ProcessRaw(map[string]interface{}{
		"name": "model-start",
		"data": map[string]interface{}{"prompt": "Assistant response", "source": "opencode"},
	}, "opencode")

	mu.Lock()
	t.Logf("After model-start: %d Hub calls", len(calls))
	if len(calls) > 0 {
		t.Logf("  Last call: status=%s activity=%s", calls[len(calls)-1].payload["status"], calls[len(calls)-1].payload["activity"])
	}
	mu.Unlock()

	// 4. Now agent finishes (agent-end → working)
	t.Log("\n--- Step 4: agent-end ---")
	calls = nil
	processor = hooks.NewHarnessProcessor()
	dialects.RegisterBuiltins(processor)
	processor.AddHandler(NewStatusHandler().Handle)
	processor.AddHandler(NewLoggingHandler().Handle)
	processor.AddHandler(NewHubHandler().Handle)

	processor.ProcessRaw(map[string]interface{}{
		"name": "agent-end",
		"data": map[string]interface{}{"source": "opencode"},
	}, "opencode")

	mu.Lock()
	t.Logf("After agent-end: %d Hub calls", len(calls))
	if len(calls) > 0 {
		t.Logf("  Last call: status=%s activity=%s", calls[len(calls)-1].payload["status"], calls[len(calls)-1].payload["activity"])
	}
	mu.Unlock()

	// 5. What happens when model-start fires AGAIN (second turn)?
	t.Log("\n--- Step 5: model-start (second turn) — THIS IS WHERE IT BREAKS ---")
	calls = nil
	processor = hooks.NewHarnessProcessor()
	dialects.RegisterBuiltins(processor)
	processor.AddHandler(NewStatusHandler().Handle)
	processor.AddHandler(NewLoggingHandler().Handle)
	processor.AddHandler(NewHubHandler().Handle)

	processor.ProcessRaw(map[string]interface{}{
		"name": "model-start",
		"data": map[string]interface{}{"prompt": "Second turn response", "source": "opencode"},
	}, "opencode")

	mu.Lock()
	t.Logf("After model-start (2nd turn): %d Hub calls", len(calls))
	if len(calls) == 0 {
		t.Log("  *** NO HUB CALL — model-start was blocked by sticky check ***")
	} else {
		t.Logf("  Last call: status=%s activity=%s", calls[len(calls)-1].payload["status"], calls[len(calls)-1].payload["activity"])
	}
	mu.Unlock()

	// 6. Check final agent-info.json
	infoData, _ := os.ReadFile(tmpDir + "/agent-info.json")
	var info map[string]interface{}
	json.Unmarshal(infoData, &info)
	t.Logf("\nFinal agent-info.json: activity=%q phase=%q", info["activity"], info["phase"])
}
