/*
Copyright 2025 The Scion Authors.
*/

package dialects

import (
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hooks"
)

// OpenCodeDialect parses events emitted by the scion-plugin.js OpenCode plugin.
//
// The plugin emits pre-normalized events in the following format:
//
//	{
//	  "name": "tool-start" | "tool-end" | "session-start" | etc.,
//	  "tool_name": "...",
//	  "prompt": "...",
//	  "success": true,
//	  ...
//	}
//
// Unlike Claude/Gemini/Codex dialects which normalize harness-specific event
// names (e.g., "PreToolUse" → "tool-start"), the opencode dialect receives
// already-normalized event names and passes them through directly.
type OpenCodeDialect struct{}

// NewOpenCodeDialect creates a new OpenCode dialect parser.
func NewOpenCodeDialect() *OpenCodeDialect {
	return &OpenCodeDialect{}
}

// Name returns the dialect name.
func (d *OpenCodeDialect) Name() string {
	return "opencode"
}

// Parse converts OpenCode plugin event format to normalized Event.
func (d *OpenCodeDialect) Parse(data map[string]interface{}) (*hooks.Event, error) {
	// The opencode plugin emits pre-normalized event names at the top level.
	// Check for "name" field first (plugin format), then fall back to
	// "hook_event_name" for compatibility with direct sciontool hook usage.
	rawName := getString(data, "name")
	if rawName == "" {
		rawName = getString(data, "hook_event_name")
	}

	// Extract data from nested "data" field if present (plugin format)
	// Otherwise use the top-level data directly (direct hook usage).
	payload := data
	if nested, ok := data["data"]; ok {
		if m, ok := nested.(map[string]interface{}); ok && len(m) > 0 {
			payload = m
		}
	}

	event := &hooks.Event{
		Name:    d.normalizeEventName(rawName),
		RawName: rawName,
		Dialect: "opencode",
		Data: hooks.EventData{
			Prompt:    getString(payload, "prompt"),
			ToolName:  getString(payload, "tool_name"),
			Message:   getString(payload, "message"),
			Reason:    getString(payload, "reason"),
			Source:    getString(payload, "source"),
			SessionID: getString(payload, "session_id"),
			Success:   getBool(payload, "success"),
			Error:     getString(payload, "error"),
			Raw:       payload,
		},
	}

	// Extract tool input/output if available
	if val, ok := payload["tool_input"]; ok {
		if str, ok := val.(string); ok {
			event.Data.ToolInput = str
		}
	}
	if val, ok := payload["tool_output"]; ok {
		if str, ok := val.(string); ok {
			event.Data.ToolOutput = str
		}
	}

	// Extract token usage
	extractTokens(payload, &event.Data)

	// Extract file_path
	extractFilePath(payload, &event.Data)

	return event, nil
}

// normalizeEventName passes through pre-normalized event names from the plugin.
// The opencode plugin already emits normalized names, so this is a no-op
// except for mapping internal control events.
func (d *OpenCodeDialect) normalizeEventName(name string) string {
	// Map internal control events to standard event names
	switch name {
	case "_activity":
		// Activity-only update — handled specially by StatusHandler
		// Return empty to let the handler process it
		return ""
	case "model-start", "model-end":
		// Model events are already normalized
		return name
	default:
		// Pass through all known event names
		return name
	}
}
