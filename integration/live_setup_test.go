package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	liveAuthVariable  = "CODEX_SDK_LIVE_AUTH_FILE"
	liveModelVariable = "CODEX_SDK_LIVE_MODEL"
	maxAuthBytes      = 1 << 20
)

type liveSettings struct {
	authPath string
	model    string
}

// readLiveSettings checks the opt-in before any filesystem or CLI access.
func readLiveSettings(lookup func(string) (string, bool)) (liveSettings, bool, error) {
	path, enabled := lookup(liveAuthVariable)
	if !enabled {
		return liveSettings{}, false, nil
	}
	if strings.TrimSpace(path) == "" {
		return liveSettings{}, true, errors.New("live auth path is empty")
	}
	model, _ := lookup(liveModelVariable)
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`).MatchString(model) {
		return liveSettings{}, true, errors.New("an explicit live model is required")
	}
	return liveSettings{authPath: path, model: model}, true, nil
}

// readLiveAuth never includes credential bytes, a private path or parser errors
// in diagnostics. The file must be outside the SDK checkout after symlink resolution.
func readLiveAuth(path, checkout string) ([]byte, string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", errors.New("live auth file is unavailable")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, "", errors.New("live auth path cannot be resolved")
	}
	rel, err := filepath.Rel(checkout, resolved)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, "", errors.New("live auth file must be outside the checkout")
	}
	entry, statErr := os.Stat(resolved)
	if statErr != nil || !entry.Mode().IsRegular() || entry.Size() > maxAuthBytes {
		return nil, "", errors.New("live auth file must be a bounded regular file")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, "", errors.New("live auth file cannot be read")
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() > maxAuthBytes {
		closeErr := file.Close()
		if closeErr != nil {
			return nil, "", errors.New("live auth file cannot be closed")
		}
		return nil, "", errors.New("live auth file must be a bounded regular file")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxAuthBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > maxAuthBytes {
		return nil, "", errors.New("live auth file cannot be read completely")
	}
	if err := validateLiveAuth(data); err != nil {
		return nil, "", err
	}
	return data, resolved, nil
}

func validateLiveAuth(data []byte) error {
	var auth struct {
		Mode   string `json:"auth_mode"`      //nolint:tagliatelle // Codex auth.json uses these wire field names.
		APIKey string `json:"OPENAI_API_KEY"` //nolint:tagliatelle // Codex auth.json uses these wire field names.
		Tokens struct {
			Access  string `json:"access_token"`  //nolint:tagliatelle // Codex auth.json uses these wire field names.
			ID      string `json:"id_token"`      //nolint:tagliatelle // Codex auth.json uses these wire field names.
			Refresh string `json:"refresh_token"` //nolint:tagliatelle // Codex auth.json uses these wire field names.
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &auth) != nil {
		return errors.New("live auth file is not valid JSON")
	}
	// Older ChatGPT auth files can omit auth_mode. Token contents are opaque;
	// actual authentication is validated by the backend, not by this parser.
	if auth.APIKey != "" || (auth.Mode != "" && auth.Mode != "chatgpt") ||
		auth.Tokens.Access == "" || auth.Tokens.ID == "" || auth.Tokens.Refresh == "" {
		return errors.New("live auth file must contain ChatGPT credentials only")
	}
	return nil
}

func liveCheckout() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("cannot locate integration sources")
	}
	return filepath.Dir(filepath.Dir(file))
}

func clearLiveEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !isLiveEnvironmentOverride(key) {
			continue
		}
		t.Setenv(key, "") // Register restoration of the original value.
		if err := os.Unsetenv(key); err != nil {
			t.Fatal("cannot isolate live environment")
		}
	}
}

func isLiveEnvironmentOverride(key string) bool {
	if key == liveAuthVariable || key == liveModelVariable {
		return false
	}
	upper := strings.ToUpper(key)
	for _, prefix := range []string{"OPENAI_", "CODEX_", "CHATGPT_", "AZURE_OPENAI_", "XDG_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	switch upper {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR":
		return true
	default:
		return false
	}
}

// requireLiveBackend is the first operation of every live test. Tests using it
// are sequential: t.Setenv and t.Chdir deliberately forbid parallel execution.
func requireLiveBackend(t *testing.T) {
	t.Helper()
	settings, enabled, err := readLiveSettings(os.LookupEnv)
	if !enabled {
		t.Skip("live backend disabled: CODEX_SDK_LIVE_AUTH_FILE is not set")
	}
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" {
		t.Fatal("live backend harness currently supports Linux only")
	}
	data, source, err := readLiveAuth(settings.authPath, liveCheckout())
	if err != nil {
		t.Fatal(err)
	}
	// Keep the comparison local; never print or archive an auth file hash.
	originalHash := sha256.Sum256(data)
	t.Cleanup(func() {
		current, readErr := os.ReadFile(source)
		if readErr != nil || sha256.Sum256(current) != originalHash {
			t.Error("source auth file changed or became unavailable during the test")
		}
	})
	cliPath, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("real Codex CLI is unavailable after live opt-in")
	}
	cliPath, err = filepath.EvalSymlinks(cliPath)
	if err != nil {
		t.Fatal("cannot resolve the Codex executable")
	}
	cliPath, err = filepath.Abs(cliPath)
	if err != nil {
		t.Fatal("cannot resolve the Codex executable")
	}
	// Require a native Linux executable, not the Python/shell offline fixture.
	// Its identity is recorded below; this is not a signature/provenance check.
	binary, err := elf.Open(cliPath)
	if err != nil {
		t.Fatal("live tests require a native Linux Codex executable, not a script or fixture")
	}
	if closeErr := binary.Close(); closeErr != nil {
		t.Fatal("cannot close Codex executable metadata")
	}
	file, err := os.Open(cliPath)
	if err != nil {
		t.Fatal("cannot read Codex executable")
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatal("cannot fingerprint Codex executable")
	}
	clearLiveEnvironment(t)
	root := t.TempDir()
	if chmodErr := os.Chmod(root, 0700); chmodErr != nil {
		t.Fatal("cannot protect live test directory")
	}
	home, work, bin, err := prepareLiveDirectories(root, data, settings.model)
	if err != nil {
		t.Fatal(err)
	}
	if linkErr := os.Symlink(cliPath, filepath.Join(bin, "codex")); linkErr != nil {
		t.Fatal("cannot pin Codex executable for live tests")
	}
	t.Setenv("CODEX_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(work)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, cliPath, "--version")
	// Discard stderr: diagnostics from authentication/configuration are not evidence fields.
	version, err := command.Output()
	if err != nil || !regexp.MustCompile(`^codex-cli [0-9]+\.[0-9]+\.[0-9]+\s*$`).Match(bytes.TrimSpace(version)) {
		t.Fatal("Codex version check failed or returned an unsupported version format")
	}
	t.Logf("live_cli_path=%s live_cli_version=%s live_cli_sha256=%s requested_model=%s", cliPath, strings.TrimSpace(string(version)), hex.EncodeToString(hash.Sum(nil)), settings.model)
}

// prepareLiveDirectories creates no network connections and never writes the
// source credential file. It is exercised with synthetic credentials offline.
func prepareLiveDirectories(root string, data []byte, model string) (home, work, bin string, err error) {
	home = filepath.Join(root, "codex-home")
	work = filepath.Join(root, "work")
	bin = filepath.Join(root, "bin")
	for _, dir := range []string{home, work, bin} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return "", "", "", errors.New("cannot create private live test directory")
		}
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), data, 0600); err != nil {
		return "", "", "", errors.New("cannot prepare private live credentials")
	}
	config := fmt.Sprintf("model = %q\nmodel_provider = \"openai\"\nforced_login_method = \"chatgpt\"\ncli_auth_credentials_store = \"file\"\nsandbox_mode = \"read-only\"\napproval_policy = \"on-request\"\nproject_doc_max_bytes = 0\n", model)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		return "", "", "", errors.New("cannot prepare isolated Codex configuration")
	}
	return home, work, bin, nil
}
