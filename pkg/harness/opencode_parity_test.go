// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// seedOpenCodeDir seeds the embedded OpenCode harness-config into a temp dir
// using the same code path operators run during scion init / harness-config
// upgrade. It returns the absolute target dir so tests can inspect it.
func seedOpenCodeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := config.SeedHarnessConfig(dir, &OpenCode{}, false); err != nil {
		t.Fatalf("SeedHarnessConfig: %v", err)
	}
	return dir
}

// TestOpenCodeEmbedsSeedRootSupportFiles verifies the new provision.py and
// the existing opencode.json land where Phase 1 said they should: provision.py
// at the harness-config root, opencode.json under home/.config/opencode/.
func TestOpenCodeEmbedsSeedRootSupportFiles(t *testing.T) {
	dir := seedOpenCodeDir(t)

	// provision.py is a root-level support file (Phase 1 allowlist).
	provPath := filepath.Join(dir, "provision.py")
	if _, err := os.Stat(provPath); err != nil {
		t.Fatalf("expected provision.py at harness-config root: %v", err)
	}

	// opencode.json is the harness-native settings file; it lives under home.
	opencodeJSON := filepath.Join(dir, "home", ".config", "opencode", "opencode.json")
	if _, err := os.Stat(opencodeJSON); err != nil {
		t.Fatalf("expected opencode.json under home/.config/opencode/: %v", err)
	}

	// config.yaml at the root must be valid and declare the container-script
	// provisioner so the in-container provision.py runs during pre-start and
	// handles MCP translation, auth, etc.
	hc, err := config.LoadHarnessConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadHarnessConfigDir: %v", err)
	}
	if hc.Config.Provisioner == nil {
		t.Fatal("expected provisioner block in seeded config.yaml")
	}
	if hc.Config.Provisioner.Type != "container-script" {
		t.Errorf("provisioner.type=%q want container-script", hc.Config.Provisioner.Type)
	}
	if len(hc.Config.Provisioner.Command) == 0 {
		t.Error("expected provisioner.command in config.yaml")
	}

	// scion-plugin.js must be seeded under home/.config/opencode/ so the
	// in-container provision.py can copy it to the plugins directory.
	pluginPath := filepath.Join(dir, "home", ".config", "opencode", "scion-plugin.js")
	if _, err := os.Stat(pluginPath); err != nil {
		t.Fatalf("expected scion-plugin.js under home/.config/opencode/: %v", err)
	}
	pluginBytes, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("failed to read scion-plugin.js: %v", err)
	}
	if !strings.Contains(string(pluginBytes), "SCION_AGENT_ID") {
		t.Error("scion-plugin.js does not contain expected SCION_AGENT_ID check")
	}
	if !strings.Contains(string(pluginBytes), "sendHook") {
		t.Error("scion-plugin.js does not contain expected sendHook function")
	}
}

// TestOpenCodeActivateScriptIsIdempotent verifies that --activate-script is a
// no-op when the embedded default already declares container-script. Existing
// installations that still have type: builtin can
// run `scion harness-config upgrade opencode --activate-script` to migrate,
// but freshly seeded configs must not produce a spurious activate_script action.
func TestOpenCodeActivateScriptIsIdempotent(t *testing.T) {
	dir := seedOpenCodeDir(t)

	plan, err := config.UpgradeHarnessConfig(dir, &OpenCode{}, config.HarnessConfigUpgradeOptions{
		ActivateScript: true,
		Now:            func() time.Time { return time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("UpgradeHarnessConfig --activate-script: %v", err)
	}

	// The activate_script action must not be present when already container-script.
	for _, action := range plan.Actions {
		if action.Type == "activate_script" {
			t.Fatalf("expected no activate_script action (already container-script), got actions: %v", plan.Actions)
		}
	}

	hc, err := config.LoadHarnessConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadHarnessConfigDir after activate: %v", err)
	}
	if hc.Config.Provisioner == nil || hc.Config.Provisioner.Type != "container-script" {
		t.Fatalf("provisioner.type=%q want container-script", hc.Config.Provisioner.Type)
	}
}

// TestOpenCodeContainerScriptHarnessParity asserts the ContainerScriptHarness
// wrapper produces the same observable command/env/capability/getter values as
// the compiled OpenCode harness for the embedded config. Parity is the
// acceptance gate from Phase 0; this test makes it executable for OpenCode.
func TestOpenCodeContainerScriptHarnessParity(t *testing.T) {
	dir := seedOpenCodeDir(t)

	hc, err := config.LoadHarnessConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadHarnessConfigDir: %v", err)
	}
	scripted, err := NewContainerScriptHarness(dir, hc.Config)
	if err != nil {
		t.Fatalf("NewContainerScriptHarness: %v", err)
	}
	builtin := &OpenCode{}

	// 1. Name must match — both report "opencode" so dispatch logic stays consistent.
	if scripted.Name() != builtin.Name() {
		t.Errorf("Name parity: scripted=%q builtin=%q", scripted.Name(), builtin.Name())
	}
	if scripted.DefaultConfigDir() != builtin.DefaultConfigDir() {
		t.Errorf("DefaultConfigDir: scripted=%q builtin=%q", scripted.DefaultConfigDir(), builtin.DefaultConfigDir())
	}
	if scripted.SkillsDir() != builtin.SkillsDir() {
		t.Errorf("SkillsDir: scripted=%q builtin=%q", scripted.SkillsDir(), builtin.SkillsDir())
	}
	if scripted.GetInterruptKey() != builtin.GetInterruptKey() {
		t.Errorf("GetInterruptKey: scripted=%q builtin=%q", scripted.GetInterruptKey(), builtin.GetInterruptKey())
	}

	// 2. GetCommand must match across the three operative shapes.
	cases := []struct {
		name    string
		task    string
		resume  bool
		baseArg []string
	}{
		{"resume_no_task", "", true, nil},
		{"task_only", "fix the bug", false, nil},
		{"task_with_base_args", "do it", false, []string{"--debug"}},
	}
	for _, tc := range cases {
		t.Run("GetCommand_"+tc.name, func(t *testing.T) {
			gotS := scripted.GetCommand(tc.task, tc.resume, tc.baseArg)
			gotB := builtin.GetCommand(tc.task, tc.resume, tc.baseArg)
			if strings.Join(gotS, " ") != strings.Join(gotB, " ") {
				t.Errorf("scripted=%v builtin=%v", gotS, gotB)
			}
		})
	}

	// 3. AdvancedCapabilities must report the same shape; the embedded YAML
	// is the single source of truth for both, so any drift indicates a bug
	// in either the YAML mapping or the compiled getter.
	gotCaps := scripted.AdvancedCapabilities()
	wantCaps := builtin.AdvancedCapabilities()
	if gotCaps.Harness != wantCaps.Harness {
		t.Errorf("Capabilities.Harness: scripted=%q builtin=%q", gotCaps.Harness, wantCaps.Harness)
	}
	if gotCaps.Limits.MaxDuration.Support != wantCaps.Limits.MaxDuration.Support {
		t.Errorf("Capabilities.Limits.MaxDuration: scripted=%v builtin=%v", gotCaps.Limits.MaxDuration, wantCaps.Limits.MaxDuration)
	}
	if gotCaps.Auth.APIKey.Support != wantCaps.Auth.APIKey.Support {
		t.Errorf("Capabilities.Auth.APIKey: scripted=%v builtin=%v", gotCaps.Auth.APIKey, wantCaps.Auth.APIKey)
	}
	if gotCaps.Auth.AuthFile.Support != wantCaps.Auth.AuthFile.Support {
		t.Errorf("Capabilities.Auth.AuthFile: scripted=%v builtin=%v", gotCaps.Auth.AuthFile, wantCaps.Auth.AuthFile)
	}
	if gotCaps.Auth.VertexAI.Support != wantCaps.Auth.VertexAI.Support {
		t.Errorf("Capabilities.Auth.VertexAI: scripted=%v builtin=%v", gotCaps.Auth.VertexAI, wantCaps.Auth.VertexAI)
	}
	if gotCaps.Prompts.SystemPrompt.Support != wantCaps.Prompts.SystemPrompt.Support {
		t.Errorf("Capabilities.Prompts.SystemPrompt: scripted=%v builtin=%v", gotCaps.Prompts.SystemPrompt, wantCaps.Prompts.SystemPrompt)
	}
}

// TestOpenCodeContainerScriptHarnessStagesScript verifies Provision() copies
// the seeded provision.py into the agent bundle and writes a wrapper that
// targets sciontool harness provision. The bundle is what the in-container
// hook actually runs.
func TestOpenCodeContainerScriptHarnessStagesScript(t *testing.T) {
	dir := seedOpenCodeDir(t)

	hc, err := config.LoadHarnessConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadHarnessConfigDir: %v", err)
	}
	scripted, err := NewContainerScriptHarness(dir, hc.Config)
	if err != nil {
		t.Fatalf("NewContainerScriptHarness: %v", err)
	}

	agentHome := t.TempDir()
	if err := scripted.Provision(context.Background(), "researcher", agentHome, agentHome, "/workspace"); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	bundle := filepath.Join(agentHome, ".scion", "harness")
	stagedScript := filepath.Join(bundle, "provision.py")
	if _, err := os.Stat(stagedScript); err != nil {
		t.Fatalf("provision.py not staged into bundle: %v", err)
	}

	// The staged script must be byte-identical to the source-of-truth in the
	// seeded harness-config dir, otherwise upgrade workflows will silently
	// drift container behavior away from the hub artifact.
	stagedBytes, err := os.ReadFile(stagedScript)
	if err != nil {
		t.Fatal(err)
	}
	srcBytes, err := os.ReadFile(filepath.Join(dir, "provision.py"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stagedBytes) != string(srcBytes) {
		t.Error("staged provision.py differs from harness-config copy")
	}

	wrapper := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d", "20-harness-provision")
	wrapperBytes, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatalf("hook wrapper missing: %v", err)
	}
	if !strings.Contains(string(wrapperBytes), "sciontool harness provision") {
		t.Errorf("wrapper does not invoke sciontool harness provision: %s", wrapperBytes)
	}
}

// TestOpenCodeContainerScriptReconcilesMissingBundle verifies that calling
// Provision() on an agent home that lacks the container-script bundle (as
// happens for agents provisioned before the builtin→container-script
// migration) stages the hook wrapper, provision.py, and manifest. This
// mirrors the reconciliation path added in run.go.
func TestOpenCodeContainerScriptReconcilesMissingBundle(t *testing.T) {
	dir := seedOpenCodeDir(t)

	hc, err := config.LoadHarnessConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadHarnessConfigDir: %v", err)
	}
	scripted, err := NewContainerScriptHarness(dir, hc.Config)
	if err != nil {
		t.Fatalf("NewContainerScriptHarness: %v", err)
	}

	agentHome := t.TempDir()

	// Simulate an agent home created by the builtin OpenCode{} harness:
	// the config dir exists but there is no .scion/harness/ bundle and no
	// pre-start hook wrapper.
	configDir := filepath.Join(agentHome, ".config", "opencode")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Confirm the hook wrapper does NOT exist yet.
	hookWrapper := filepath.Join(agentHome, ".scion", "hooks", "pre-start.d", "20-harness-provision")
	if _, err := os.Stat(hookWrapper); err == nil {
		t.Fatal("hook wrapper should not exist before reconciliation")
	}

	// Call Provision (the reconciliation path).
	if err := scripted.Provision(context.Background(), "migrated-agent", agentHome, agentHome, "/workspace"); err != nil {
		t.Fatalf("Provision (reconciliation): %v", err)
	}

	// Hook wrapper must now exist.
	wrapperBytes, err := os.ReadFile(hookWrapper)
	if err != nil {
		t.Fatalf("hook wrapper not staged after reconciliation: %v", err)
	}
	if !strings.Contains(string(wrapperBytes), "sciontool harness provision") {
		t.Errorf("wrapper does not invoke sciontool harness provision: %s", wrapperBytes)
	}

	// provision.py must be staged.
	if _, err := os.Stat(filepath.Join(agentHome, ".scion", "harness", "provision.py")); err != nil {
		t.Errorf("provision.py not staged: %v", err)
	}

	// manifest.json must be present.
	if _, err := os.Stat(filepath.Join(agentHome, ".scion", "harness", "manifest.json")); err != nil {
		t.Errorf("manifest.json not staged: %v", err)
	}

	// Pre-existing opencode.json must be preserved.
	if _, err := os.Stat(filepath.Join(configDir, "opencode.json")); err != nil {
		t.Errorf("pre-existing opencode.json was removed: %v", err)
	}
}

// TestOpenCodeProvisionScript_Integration_HappyPath runs the actual Python
// script against a synthetic manifest and validates outputs. We skip when
// python3 is unavailable so the test is portable, and use a tightly-scoped
// $HOME to avoid leaking host paths into resolved-auth.json.
func TestOpenCodeProvisionScript_Integration_HappyPath(t *testing.T) {
	pyPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping script integration test")
	}

	dir := seedOpenCodeDir(t)
	scriptPath := filepath.Join(dir, "provision.py")

	home := t.TempDir()
	bundle := filepath.Join(home, ".scion", "harness")
	if err := os.MkdirAll(filepath.Join(bundle, "inputs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "outputs"), 0755); err != nil {
		t.Fatal(err)
	}

	manifest := map[string]any{
		"schema_version":     1,
		"command":            "provision",
		"agent_name":         "test-agent",
		"agent_home":         home,
		"agent_workspace":    "/workspace",
		"harness_bundle_dir": bundle,
		"harness_config":     map[string]any{"harness": "opencode"},
		"inputs":             map[string]any{},
		"outputs": map[string]any{
			"env":           filepath.Join(bundle, "outputs", "env.json"),
			"resolved_auth": filepath.Join(bundle, "outputs", "resolved-auth.json"),
		},
		"platform": map[string]any{"goos": "linux", "goarch": "amd64"},
	}
	manifestPath := filepath.Join(bundle, "manifest.json")
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(manifestPath, manifestBytes, 0644); err != nil {
		t.Fatal(err)
	}

	candidates := map[string]any{
		"schema_version":  1,
		"explicit_type":   "",
		"resolved_method": "container-script",
		"env_vars":        []string{"OPENAI_API_KEY"},
		"files":           []any{},
	}
	candBytes, _ := json.MarshalIndent(candidates, "", "  ")
	if err := os.WriteFile(filepath.Join(bundle, "inputs", "auth-candidates.json"), candBytes, 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(pyPath, scriptPath, "--manifest", manifestPath)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("provision script failed: %v\noutput: %s", err, out)
	}

	resolvedBytes, err := os.ReadFile(filepath.Join(bundle, "outputs", "resolved-auth.json"))
	if err != nil {
		t.Fatalf("resolved-auth.json missing: %v\nscript output: %s", err, out)
	}
	var resolved map[string]any
	if err := json.Unmarshal(resolvedBytes, &resolved); err != nil {
		t.Fatalf("resolved-auth.json invalid: %v", err)
	}
	if resolved["method"] != "api-key" {
		t.Errorf("method=%v want api-key", resolved["method"])
	}
	if resolved["env_var"] != "OPENAI_API_KEY" {
		t.Errorf("env_var=%v want OPENAI_API_KEY (precedence: only OpenAI was offered)", resolved["env_var"])
	}

	envBytes, err := os.ReadFile(filepath.Join(bundle, "outputs", "env.json"))
	if err != nil {
		t.Fatalf("env.json missing: %v", err)
	}
	var envOverlay map[string]any
	if err := json.Unmarshal(envBytes, &envOverlay); err != nil {
		t.Fatalf("env.json invalid: %v", err)
	}
	if len(envOverlay) != 0 {
		t.Errorf("env.json should be empty for OpenCode (no overrides), got %v", envOverlay)
	}
}

// TestOpenCodeProvisionScript_Integration_MCP runs the script with a staged
// mcp-servers.json input and asserts it translates universal entries into
// OpenCode's native shape (mcp.<name>.type=local|remote, command array, etc.).
func TestOpenCodeProvisionScript_Integration_MCP(t *testing.T) {
	pyPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping script integration test")
	}

	dir := seedOpenCodeDir(t)
	scriptPath := filepath.Join(dir, "provision.py")
	// Stage scion_harness.py next to provision.py so the import in the
	// script resolves — production sets this up via ContainerScriptHarness.
	if err := os.WriteFile(filepath.Join(dir, "scion_harness.py"), SharedHarnessHelperSource(), 0644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	bundle := filepath.Join(home, ".scion", "harness")
	if err := os.MkdirAll(filepath.Join(bundle, "inputs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "outputs"), 0755); err != nil {
		t.Fatal(err)
	}
	// Copy the helper into the bundle too because production stages it there
	// (ContainerScriptHarness.Provision writes it). The integration test
	// invokes the script from the seeded harness-config dir, so the import
	// works from there as well — staging here mirrors the production layout
	// so changes to where the helper goes get caught.
	if err := os.WriteFile(filepath.Join(bundle, "scion_harness.py"), SharedHarnessHelperSource(), 0644); err != nil {
		t.Fatal(err)
	}

	manifest := map[string]any{
		"schema_version":     1,
		"command":            "provision",
		"agent_name":         "test-agent",
		"agent_home":         home,
		"agent_workspace":    "/workspace",
		"harness_bundle_dir": bundle,
		"harness_config":     map[string]any{"harness": "opencode"},
		"inputs":             map[string]any{},
		"outputs": map[string]any{
			"env":           filepath.Join(bundle, "outputs", "env.json"),
			"resolved_auth": filepath.Join(bundle, "outputs", "resolved-auth.json"),
		},
		"platform": map[string]any{"goos": "linux", "goarch": "amd64"},
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(bundle, "manifest.json"), manifestBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Auth candidates so the auth phase succeeds (script bails before MCP
	// otherwise).
	candidates := map[string]any{
		"schema_version":  1,
		"explicit_type":   "",
		"resolved_method": "container-script",
		"env_vars":        []string{"OPENAI_API_KEY"},
		"files":           []any{},
	}
	candBytes, _ := json.MarshalIndent(candidates, "", "  ")
	if err := os.WriteFile(filepath.Join(bundle, "inputs", "auth-candidates.json"), candBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Stage MCP servers — exercise stdio, SSE, and a project-scoped entry
	// (which should be silently demoted to global with a warning).
	mcp := map[string]any{
		"schema_version": 1,
		"mcp_servers": map[string]any{
			"chrome-devtools": map[string]any{
				"transport": "stdio",
				"command":   "chrome-devtools-mcp",
				"args":      []string{"--headless", "--browser-url", "http://localhost:9222"},
				"env":       map[string]string{"DEBUG": "false"},
			},
			"remote_api": map[string]any{
				"transport": "sse",
				"url":       "http://localhost:8080/mcp/sse",
				"headers":   map[string]string{"Authorization": "Bearer xyz"},
			},
			"workspace_db": map[string]any{
				"transport": "stdio",
				"command":   "db-mcp",
				"scope":     "project",
			},
		},
	}
	mcpBytes, _ := json.MarshalIndent(mcp, "", "  ")
	if err := os.WriteFile(filepath.Join(bundle, "inputs", "mcp-servers.json"), mcpBytes, 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(pyPath, scriptPath, "--manifest", filepath.Join(bundle, "manifest.json"))
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("provision script failed: %v\noutput: %s", err, out)
	}

	opencodeJSONPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	data, err := os.ReadFile(opencodeJSONPath)
	if err != nil {
		t.Fatalf("opencode.json not written: %v\nscript output: %s", err, out)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("opencode.json invalid JSON: %v", err)
	}
	mcpBlock, ok := cfg["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("opencode.json mcp block missing or wrong type: %v", cfg["mcp"])
	}

	// chrome-devtools: stdio -> local with combined command array.
	chrome, ok := mcpBlock["chrome-devtools"].(map[string]any)
	if !ok {
		t.Fatalf("chrome-devtools entry missing")
	}
	if chrome["type"] != "local" {
		t.Errorf("chrome-devtools type=%v want local", chrome["type"])
	}
	cmdArr, ok := chrome["command"].([]any)
	if !ok {
		t.Fatalf("chrome-devtools command is not an array: %T", chrome["command"])
	}
	wantCmd := []string{"chrome-devtools-mcp", "--headless", "--browser-url", "http://localhost:9222"}
	if len(cmdArr) != len(wantCmd) {
		t.Errorf("chrome-devtools command length=%d want %d (got %v)", len(cmdArr), len(wantCmd), cmdArr)
	}
	for i, c := range cmdArr {
		if i >= len(wantCmd) {
			break
		}
		if c != wantCmd[i] {
			t.Errorf("chrome-devtools command[%d]=%v want %v", i, c, wantCmd[i])
		}
	}
	envMap, ok := chrome["environment"].(map[string]any)
	if !ok || envMap["DEBUG"] != "false" {
		t.Errorf("chrome-devtools environment=%v want DEBUG=false", chrome["environment"])
	}

	// remote_api: sse -> remote with url and headers.
	remote, ok := mcpBlock["remote_api"].(map[string]any)
	if !ok {
		t.Fatalf("remote_api entry missing")
	}
	if remote["type"] != "remote" {
		t.Errorf("remote_api type=%v want remote", remote["type"])
	}
	if remote["url"] != "http://localhost:8080/mcp/sse" {
		t.Errorf("remote_api url=%v", remote["url"])
	}
	headers, ok := remote["headers"].(map[string]any)
	if !ok || headers["Authorization"] != "Bearer xyz" {
		t.Errorf("remote_api headers=%v", remote["headers"])
	}

	// workspace_db: project-scoped stdio, treated as global with warning.
	if _, ok := mcpBlock["workspace_db"]; !ok {
		t.Errorf("workspace_db entry missing (project-scoped should be demoted to global, not dropped)")
	}
	if !strings.Contains(string(out), "project scope") {
		t.Errorf("expected project-scope warning in stderr, got: %s", out)
	}
	if !strings.Contains(string(out), "applied 3 mcp server(s)") {
		t.Errorf("expected 'applied 3 mcp server(s)' summary, got: %s", out)
	}
}

// TestOpenCodeProvisionScript_Integration_NoCreds asserts the script exits
// non-zero with an actionable message when nothing is staged. This matches
// the compiled harness's pre-launch failure mode.
func TestOpenCodeProvisionScript_Integration_NoCreds(t *testing.T) {
	pyPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping script integration test")
	}

	dir := seedOpenCodeDir(t)
	scriptPath := filepath.Join(dir, "provision.py")

	home := t.TempDir()
	bundle := filepath.Join(home, ".scion", "harness")
	if err := os.MkdirAll(filepath.Join(bundle, "inputs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "outputs"), 0755); err != nil {
		t.Fatal(err)
	}

	manifest := map[string]any{
		"schema_version":     1,
		"command":            "provision",
		"agent_name":         "test-agent",
		"agent_home":         home,
		"agent_workspace":    "/workspace",
		"harness_bundle_dir": bundle,
		"harness_config":     map[string]any{"harness": "opencode"},
		"inputs":             map[string]any{},
		"outputs": map[string]any{
			"env":           filepath.Join(bundle, "outputs", "env.json"),
			"resolved_auth": filepath.Join(bundle, "outputs", "resolved-auth.json"),
		},
	}
	manifestBytes, _ := json.Marshal(manifest)
	manifestPath := filepath.Join(bundle, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0644); err != nil {
		t.Fatal(err)
	}

	candidates := map[string]any{
		"schema_version":  1,
		"explicit_type":   "",
		"resolved_method": "container-script",
		"env_vars":        []string{},
		"files":           []any{},
	}
	candBytes, _ := json.Marshal(candidates)
	if err := os.WriteFile(filepath.Join(bundle, "inputs", "auth-candidates.json"), candBytes, 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(pyPath, scriptPath, "--manifest", manifestPath)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit, got success. output: %s", out)
	}
	if !strings.Contains(string(out), "no valid auth method") {
		t.Errorf("expected actionable no-creds message, got: %s", out)
	}
}

// TestOpenCodeContainerScriptResolveAuthShape verifies the container-script
// ResolveAuth surfaces the values the script will need (env keys + files)
// while never returning the original Method strings the runtime gates on.
// This protects callers like applyResolvedAuth that branch on Method.
func TestOpenCodeContainerScriptResolveAuthShape(t *testing.T) {
	dir := seedOpenCodeDir(t)

	hc, err := config.LoadHarnessConfigDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	scripted, err := NewContainerScriptHarness(dir, hc.Config)
	if err != nil {
		t.Fatal(err)
	}

	// Pass both an Anthropic key and an auth file; the container-script
	// wrapper must surface BOTH so the in-container script can choose,
	// whereas the compiled harness would have collapsed to one.
	resolved, err := scripted.ResolveAuth(api.AuthConfig{
		AnthropicAPIKey:  "sk-ant-xx",
		OpenCodeAuthFile: "/tmp/auth.json",
	})
	if err != nil {
		t.Fatalf("ResolveAuth: %v", err)
	}
	if resolved.Method != "container-script" {
		t.Errorf("Method=%q want container-script (final selection deferred to script)", resolved.Method)
	}
	if resolved.EnvVars["ANTHROPIC_API_KEY"] != "sk-ant-xx" {
		t.Errorf("expected ANTHROPIC_API_KEY to flow through, got %v", resolved.EnvVars)
	}
	foundOpenCodeAuthFile := false
	for _, f := range resolved.Files {
		if f.SourcePath == "/tmp/auth.json" && strings.HasSuffix(f.ContainerPath, "/auth.json") {
			foundOpenCodeAuthFile = true
		}
	}
	if !foundOpenCodeAuthFile {
		t.Errorf("expected OpenCode auth file in Files mapping, got %#v", resolved.Files)
	}
}
