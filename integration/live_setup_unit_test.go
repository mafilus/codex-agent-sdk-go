package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
	"github.com/stretchr/testify/require"
)

const syntheticChatGPTAuth = `{"auth_mode":"chatgpt","tokens":{"id_token":"SENTINEL-ID","access_token":"SENTINEL-ACCESS","refresh_token":"SENTINEL-REFRESH"}}`

func TestLiveSettingsOptIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  map[string]string
		enabled bool
		invalid bool
	}{
		{name: "absent", values: map[string]string{}},
		{name: "empty", values: map[string]string{liveAuthVariable: ""}, enabled: true, invalid: true},
		{name: "missing-model", values: map[string]string{liveAuthVariable: "/private/auth.json"}, enabled: true, invalid: true},
		{name: "model-newline", values: map[string]string{liveAuthVariable: "/private/auth.json", liveModelVariable: "model\n"}, enabled: true, invalid: true},
		{name: "explicit", values: map[string]string{liveAuthVariable: "/private/auth.json", liveModelVariable: "chosen-model"}, enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			settings, enabled, err := readLiveSettings(func(key string) (string, bool) {
				calls = append(calls, key)
				value, ok := tc.values[key]
				return value, ok
			})
			require.Equal(t, tc.enabled, enabled)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if !enabled {
				require.Equal(t, []string{liveAuthVariable}, calls, "no fallback lookup before opt-in")
			} else if err == nil {
				require.Equal(t, "chosen-model", settings.model)
			}
		})
	}
}

func TestLiveAuthValidationDoesNotDiscloseInput(t *testing.T) {
	for _, data := range []string{
		`SENTINEL-SECRET not JSON`,
		`{"OPENAI_API_KEY":"SENTINEL-SECRET"}`,
		`{"auth_mode":"api_key","tokens":{"access_token":"SENTINEL-SECRET"}}`,
		`{"auth_mode":"chatgpt","tokens":{"id_token":"SENTINEL-SECRET"}}`,
	} {
		err := validateLiveAuth([]byte(data))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "SENTINEL")
	}
	require.NoError(t, validateLiveAuth([]byte(syntheticChatGPTAuth)))
}

func TestLiveAuthFileBoundaryAndPrivateCopy(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	require.NoError(t, os.Mkdir(checkout, 0700))
	inside := filepath.Join(checkout, "auth.json")
	require.NoError(t, os.WriteFile(inside, []byte(syntheticChatGPTAuth), 0600))
	_, _, err := readLiveAuth(inside, checkout)
	require.ErrorContains(t, err, "outside the checkout")
	alias := filepath.Join(t.TempDir(), "alias.json")
	require.NoError(t, os.Symlink(inside, alias))
	_, _, err = readLiveAuth(alias, checkout)
	require.ErrorContains(t, err, "outside the checkout")

	outside := filepath.Join(t.TempDir(), "source.json")
	original := []byte(syntheticChatGPTAuth)
	require.NoError(t, os.WriteFile(outside, original, 0600))
	data, resolved, err := readLiveAuth(outside, checkout)
	require.NoError(t, err)
	require.Equal(t, outside, resolved)
	home, work, bin, err := prepareLiveDirectories(t.TempDir(), data, "explicit-model")
	require.NoError(t, err)
	for _, dir := range []string{home, work, bin} {
		info, statErr := os.Stat(dir)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	}
	info, err := os.Stat(filepath.Join(home, "auth.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.NoError(t, os.WriteFile(filepath.Join(home, "auth.json"), []byte("refreshed copy"), 0600))
	unchanged, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.True(t, bytes.Equal(original, unchanged), "source credentials must stay unchanged")
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(config), `model = "explicit-model"`)
	require.Contains(t, string(config), `forced_login_method = "chatgpt"`)
}

func TestLiveEnvironmentAndErrorDiagnostics(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "CODEX_HOME", "CODEX_CLI_SKIP_VERSION_CHECK", "HTTPS_PROXY", "https_proxy"} {
		t.Setenv(key, "SENTINEL-SECRET")
	}
	t.Run("isolated", func(t *testing.T) {
		clearLiveEnvironment(t)
		for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "CODEX_HOME", "CODEX_CLI_SKIP_VERSION_CHECK", "HTTPS_PROXY", "https_proxy"} {
			_, present := os.LookupEnv(key)
			require.False(t, present, key)
		}
	})
	require.Equal(t, "SENTINEL-SECRET", os.Getenv("OPENAI_API_KEY"), "restore caller environment")
	err := safeLiveError(errors.New("SENTINEL-SECRET /private/auth.json"))
	require.NotContains(t, fmt.Sprintf("%+v", err), "SENTINEL")
	require.NotContains(t, err.Error(), "/private")
	require.ErrorIs(t, safeLiveError(context.Canceled), context.Canceled)

	t.Setenv(liveModelVariable, "explicit-model")
	var options codexsdk.CodexAgentOptions
	var stderr string
	for _, option := range liveOptions(codexsdk.WithStderr(func(line string) { stderr = line })) {
		option(&options)
	}
	options.Stderr("SENTINEL-SECRET")
	require.NotContains(t, stderr, "SENTINEL")
	require.Equal(t, "explicit-model", options.Model)
	require.Equal(t, "read-only", options.Sandbox)
}

// A separate test process makes actual t.Skip/t.Fatal outcomes observable. Its
// PATH contains a sentinel CLI which must never execute on any boundary below.
func TestLiveOptInBoundaries(t *testing.T) {
	if os.Getenv("SDK_LIVE_BOUNDARY_CHILD") == "1" {
		requireLiveBackend(t)
		t.Fatal("boundary probe unexpectedly reached backend execution")
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "launched")
	cli := filepath.Join(bin, "codex")
	require.NoError(t, os.WriteFile(cli, []byte("#!/bin/sh\nprintf launched > '"+marker+"'\nexit 9\n"), 0700))
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		env  []string
		skip bool
	}{
		{name: "absent", skip: true},
		{name: "empty", env: []string{liveAuthVariable + "="}},
		{name: "missing-file", env: []string{liveAuthVariable + "=/nonexistent/private/SENTINEL-SECRET.json", liveModelVariable + "=model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestLiveOptInBoundaries$", "-test.v")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key != liveAuthVariable && key != liveModelVariable && key != "SDK_LIVE_BOUNDARY_CHILD" && key != "PATH" {
					command.Env = append(command.Env, entry)
				}
			}
			command.Env = append(command.Env, "SDK_LIVE_BOUNDARY_CHILD=1", "PATH="+bin)
			command.Env = append(command.Env, tc.env...)
			output, runErr := command.CombinedOutput()
			if tc.skip {
				require.NoError(t, runErr)
				require.Contains(t, string(output), "--- SKIP:")
			} else {
				require.Error(t, runErr)
				require.NotContains(t, string(output), "--- SKIP:")
			}
			require.NotContains(t, string(output), "SENTINEL-SECRET")
			_, statErr := os.Stat(marker)
			require.ErrorIs(t, statErr, os.ErrNotExist, "boundary must not launch CLI")
		})
	}
}

func TestAllLiveEntryPointsAreGated(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(liveCheckout(), "integration", "*_test.go"))
	require.NoError(t, err)
	seen := make(map[string]bool)
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		if !bytes.HasPrefix(data, []byte("//go:build integration")) {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, data, 0)
		require.NoError(t, parseErr)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			seen[fn.Name.Name] = true
			require.NotEmpty(t, fn.Body.List, fn.Name.Name)
			statement, ok := fn.Body.List[0].(*ast.ExprStmt)
			require.True(t, ok, fn.Name.Name)
			call, ok := statement.X.(*ast.CallExpr)
			require.True(t, ok, fn.Name.Name)
			name, ok := call.Fun.(*ast.Ident)
			require.True(t, ok, fn.Name.Name)
			require.Equal(t, "requireLiveBackend", name.Name, fn.Name.Name)
		}
	}
	for _, name := range []string{
		"TestAgentsAndSettings_AgentDefinition", "TestAgentsAndSettings_SettingSources", "TestAgentsAndSettings_NoSettingSources", "TestAgentsAndSettings_FilesystemAgentLoading",
		"TestStderrCallback_ReceivesOutput", "TestStderrCallback_CapturesDebugInfo",
		"TestDynamicControl_SetPermissionMode", "TestDynamicControl_SetModel", "TestDynamicControl_Interrupt",
		"TestQuery_CloseMidStream", "TestClient_ContextCancelDuringQuery", "TestClient_RapidCloseReopen",
		"TestMCPTools_Registration", "TestSDKTools_Registration", "TestSDKTools_Execution", "TestSDKTools_ReturnValue",
		"TestPlanMode_UserInputCallback", "TestPlanMode_StartWithPrompt", "TestPartialMessages_DisabledByDefault",
		"TestStructuredOutput_JSONSchema", "TestStructuredOutput_RequiredFields", "TestStructuredOutput_WithEnum",
	} {
		require.True(t, seen[name], "existing live test missing from inventory: %s", name)
	}
}
