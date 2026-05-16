/*
Copyright 2025 The Scion Authors.
*/

package dialects

import (
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hooks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeDialect_Name(t *testing.T) {
	d := NewOpenCodeDialect()
	assert.Equal(t, "opencode", d.Name())
}

func TestOpenCodeDialect_Parse_NestedFormat(t *testing.T) {
	// Test the plugin's nested JSON format: {"name": "...", "data": {...}}
	tests := []struct {
		name       string
		input      map[string]interface{}
		wantName   string
		wantTool   string
		wantPrompt string
		wantSource string
	}{
		{
			name: "tool-start event",
			input: map[string]interface{}{
				"name": "tool-start",
				"data": map[string]interface{}{
					"tool_name": "Bash",
					"source":    "opencode",
				},
			},
			wantName:   hooks.EventToolStart,
			wantTool:   "Bash",
			wantSource: "opencode",
		},
		{
			name: "tool-end event",
			input: map[string]interface{}{
				"name": "tool-end",
				"data": map[string]interface{}{
					"tool_name": "Read",
					"success":   true,
				},
			},
			wantName: hooks.EventToolEnd,
			wantTool: "Read",
		},
		{
			name: "session-start event",
			input: map[string]interface{}{
				"name": "session-start",
				"data": map[string]interface{}{
					"source": "opencode",
				},
			},
			wantName: hooks.EventSessionStart,
		},
		{
			name: "session-end event",
			input: map[string]interface{}{
				"name": "session-end",
				"data": map[string]interface{}{
					"source": "opencode",
				},
			},
			wantName: hooks.EventSessionEnd,
		},
		{
			name: "prompt-submit event",
			input: map[string]interface{}{
				"name": "prompt-submit",
				"data": map[string]interface{}{
					"prompt": "Write a function",
					"source": "opencode",
				},
			},
			wantName:   hooks.EventPromptSubmit,
			wantPrompt: "Write a function",
		},
		{
			name: "notification event",
			input: map[string]interface{}{
				"name": "notification",
				"data": map[string]interface{}{
					"message": "Please confirm action",
					"source":  "opencode",
				},
			},
			wantName: hooks.EventNotification,
		},
		{
			name: "response-complete event",
			input: map[string]interface{}{
				"name": "response-complete",
				"data": map[string]interface{}{
					"source": "opencode",
				},
			},
			wantName: hooks.EventResponseComplete,
		},
		{
			name: "model-start event",
			input: map[string]interface{}{
				"name": "model-start",
				"data": map[string]interface{}{
					"source": "opencode",
				},
			},
			wantName: hooks.EventModelStart,
		},
		{
			name: "model-end event",
			input: map[string]interface{}{
				"name": "model-end",
				"data": map[string]interface{}{
					"source": "opencode",
				},
			},
			wantName: hooks.EventModelEnd,
		},
	}

	d := NewOpenCodeDialect()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := d.Parse(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, event.Name)
			assert.Equal(t, "opencode", event.Dialect)

			if tt.wantTool != "" {
				assert.Equal(t, tt.wantTool, event.Data.ToolName)
			}
			if tt.wantPrompt != "" {
				assert.Equal(t, tt.wantPrompt, event.Data.Prompt)
			}
			if tt.wantSource != "" {
				assert.Equal(t, tt.wantSource, event.Data.Source)
			}
		})
	}
}

func TestOpenCodeDialect_Parse_FlatFormat(t *testing.T) {
	// Test the flat JSON format (no nested data): {"name": "...", "tool_name": "..."}
	d := NewOpenCodeDialect()

	event, err := d.Parse(map[string]interface{}{
		"name":      "tool-start",
		"tool_name": "Edit",
		"source":    "opencode",
	})
	require.NoError(t, err)
	assert.Equal(t, hooks.EventToolStart, event.Name)
	assert.Equal(t, "Edit", event.Data.ToolName)
	assert.Equal(t, "opencode", event.Dialect)
}

func TestOpenCodeDialect_Parse_ToolSuccessError(t *testing.T) {
	d := NewOpenCodeDialect()

	t.Run("tool-end with success", func(t *testing.T) {
		event, err := d.Parse(map[string]interface{}{
			"name":   "tool-end",
			"data":   map[string]interface{}{"tool_name": "Bash", "success": true},
			"source": "opencode",
		})
		require.NoError(t, err)
		assert.True(t, event.Data.Success)
	})

	t.Run("tool-end with error", func(t *testing.T) {
		event, err := d.Parse(map[string]interface{}{
			"name":   "tool-end",
			"data":   map[string]interface{}{"tool_name": "Bash", "success": false, "error": "command not found"},
			"source": "opencode",
		})
		require.NoError(t, err)
		assert.False(t, event.Data.Success)
		assert.Equal(t, "command not found", event.Data.Error)
	})
}

func TestOpenCodeDialect_Parse_Heartbeat(t *testing.T) {
	d := NewOpenCodeDialect()

	t.Run("model-start heartbeat", func(t *testing.T) {
		event, err := d.Parse(map[string]interface{}{
			"name": "model-start",
			"data": map[string]interface{}{"_heartbeat": true},
		})
		require.NoError(t, err)
		assert.Equal(t, hooks.EventModelStart, event.Name)
	})

	t.Run("model-end heartbeat", func(t *testing.T) {
		event, err := d.Parse(map[string]interface{}{
			"name": "model-end",
			"data": map[string]interface{}{"_heartbeat": true},
		})
		require.NoError(t, err)
		assert.Equal(t, hooks.EventModelEnd, event.Name)
	})
}
