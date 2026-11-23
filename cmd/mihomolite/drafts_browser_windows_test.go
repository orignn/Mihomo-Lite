//go:build windows

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"gopkg.in/yaml.v3"
)

const draftBrowserGate = "MIHOMOLITE_TEST_DRAFT_BROWSER"
const draftBrowserValidator = "MIHOMOLITE_TEST_DRAFT_VALIDATOR"
const draftBrowserFixtureHeader = "X-Mihomo-Draft-Browser-Fixture"

type draftBrowserProxy struct{ calls atomic.Uint32 }

func (p *draftBrowserProxy) Enable(string, string) error {
	p.calls.Add(1)
	return errors.New("proxy activation is disabled in the browser fixture")
}

func (p *draftBrowserProxy) Disable() error {
	p.calls.Add(1)
	return errors.New("proxy restoration is disabled in the browser fixture")
}

func (*draftBrowserProxy) LastError() string { return "" }

func (p *draftBrowserProxy) Inspect() (proxySnapshot, bool, error) {
	p.calls.Add(1)
	return proxySnapshot{}, false, errors.New("proxy inspection is disabled in the browser fixture")
}

type draftBrowserProfile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	YAML         string `json:"yaml"`
	Subscription string `json:"subscription,omitempty"`
	AutoHours    int    `json:"autoHours,omitempty"`
}

type draftBrowserFixture struct {
	URL      string                `json:"url"`
	Token    string                `json:"token"`
	Version  string                `json:"version"`
	Edge     string                `json:"edge"`
	Profiles []draftBrowserProfile `json:"profiles"`
	Rule     CustomRule            `json:"rule"`
}

// The fixture installs a copy of the Go test runner at its own validator path.
// The copy accepts only the exact -t invocation within this run's temp folder.
// It checks the rendered YAML shape and cannot launch a proxy or open a listener.
func init() {
	expected := os.Getenv(draftBrowserValidator)
	if os.Getenv(draftBrowserGate) != "1" || expected == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Clean(exe), expected) {
		return
	}
	if err := checkDraftBrowserValidationArgs(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func checkDraftBrowserValidationArgs(args []string) error {
	if len(args) != 6 || args[1] != "-t" || args[2] != "-d" || args[4] != "-f" {
		return errors.New("browser fixture validator accepts only -t -d DIR -f FILE")
	}
	dir := filepath.Clean(args[3])
	temp := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(os.Getenv(draftBrowserValidator)))), "tmp")
	if !strings.EqualFold(filepath.Dir(dir), temp) || !strings.HasPrefix(filepath.Base(dir), "mihomolite-validate-") ||
		!strings.EqualFold(filepath.Clean(args[5]), filepath.Join(dir, "validate.yaml")) {
		return errors.New("browser fixture validator rejected a non-fixture path")
	}
	input, err := os.ReadFile(args[5])
	if err != nil {
		return err
	}
	var rendered struct {
		BindAddress string   `yaml:"bind-address"`
		Rules       []string `yaml:"rules"`
		TUN         struct{ Enable bool }
	}
	if err := yaml.Unmarshal(input, &rendered); err != nil {
		return err
	}
	if rendered.BindAddress != "127.0.0.1" || len(rendered.Rules) == 0 || rendered.TUN.Enable {
		return errors.New("browser fixture validator rejected rendered YAML shape")
	}
	return nil
}

// TestDraftsBrowser is intentionally skipped by the normal build and Go suite.
// Run with MIHOMOLITE_TEST_DRAFT_BROWSER=1 and go test -count=1 -v
// ./cmd/mihomolite -run '^TestDraftsBrowser$'. Node 22+ and Edge are required.
func TestDraftsBrowser(t *testing.T) {
	if os.Getenv(draftBrowserGate) != "1" {
		t.Skip("opt in with " + draftBrowserGate + "=1")
	}

	root := draftTestWorkspace(t)
	script := filepath.Join(root, "tests", "browser-drafts-smoke.cjs")
	if !fileExists(script) {
		t.Fatal("cannot locate the browser smoke script in the Mihomo Lite workspace")
	}
	node, err := exec.LookPath("node.exe")
	if err != nil {
		t.Fatalf("Node 22+ is required: %v", err)
	}
	edge := findDraftBrowserEdge(t)
	runDir := createDraftTestRunDir(t, root, "browser")
	t.Logf("isolated browser artifacts: %s", runDir)

	fixture := syntheticDraftFixture()
	fixture.Edge = edge
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	fixture.Token = hex.EncodeToString(nonce[:])

	app := seedDraftBrowserApplication(t, runDir, fixture)
	installDraftBrowserValidator(t, app.baseDir)
	server := httptest.NewUnstartedServer(draftBrowserRoutes(app, fixture.Token))
	t.Cleanup(server.Close)
	address := server.Listener.Addr().String()
	if !strings.HasPrefix(address, "127.0.0.1:") || strings.HasSuffix(address, ":17653") {
		t.Fatal("fixture must use an ephemeral IPv4 loopback port outside the daily launcher")
	}
	app.config.Listen = address
	server.Config.ReadHeaderTimeout = 5 * time.Second
	server.Start()
	fixture.URL = server.URL
	runDraftBrowserNode(t, node, script, runDir, fixture)
}

func findDraftBrowserEdge(t *testing.T) string {
	t.Helper()
	edge := os.Getenv("MIHOMOLITE_TEST_BROWSER")
	if edge != "" {
		if !filepath.IsAbs(edge) || !strings.EqualFold(filepath.Base(edge), "msedge.exe") || !fileExists(edge) {
			t.Fatal("MIHOMOLITE_TEST_BROWSER must point to an installed msedge.exe")
		}
		return filepath.Clean(edge)
	}
	for _, variable := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		if base := os.Getenv(variable); filepath.IsAbs(base) {
			candidate := filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe")
			if fileExists(candidate) {
				return candidate
			}
		}
	}
	t.Fatal("installed Edge not found; set MIHOMOLITE_TEST_BROWSER to msedge.exe")
	return ""
}

func draftTestWorkspace(t *testing.T) string {
	t.Helper()
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	if !fileExists(filepath.Join(root, "go.mod")) {
		t.Fatal("cannot locate the Mihomo Lite workspace")
	}
	return root
}

func createDraftTestRunDir(t *testing.T, root, area string) string {
	t.Helper()
	parent := root
	for _, segment := range []string{".cache", "tests", "opt-002-003", "20261120-development", area} {
		parent = filepath.Join(parent, segment)
		if err := os.Mkdir(parent, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
		name, err := windows.UTF16PtrFromString(parent)
		if err != nil {
			t.Fatal(err)
		}
		attrs, err := windows.GetFileAttributes(name)
		if err != nil || attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			t.Fatalf("unsafe fixture directory %s: %v", parent, err)
		}
	}
	dir, err := os.MkdirTemp(parent, "run-")
	if err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(dir, "tmp")
	if err := os.Mkdir(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", temp)
	t.Setenv("TMP", temp)
	return dir
}

func syntheticDraftFixture() draftBrowserFixture {
	return draftBrowserFixture{
		Version: AppVersion,
		Profiles: []draftBrowserProfile{
			{ID: strings.Repeat("a", 64), Name: "Browser fixture Alpha", YAML: draftBrowserYAML("Alpha")},
			{ID: strings.Repeat("b", 64), Name: "Browser fixture Beta", YAML: draftBrowserYAML("Beta"), Subscription: "https://browser-fixture.invalid/subscription", AutoHours: 24},
		},
		Rule: CustomRule{ID: "browser-fixture-seed", Type: "DOMAIN", Value: "seed.browser-fixture.invalid", Policy: "DIRECT", Enabled: true},
	}
}

func draftBrowserYAML(name string) string {
	return fmt.Sprintf("# Synthetic browser fixture %s\nmode: rule\nproxies:\n  - name: Fixture %s\n    type: socks5\n    server: 192.0.2.1\n    port: 9\nrules:\n  - MATCH,DIRECT\n", name, name)
}

func seedDraftBrowserApplication(t *testing.T, runDir string, fixture draftBrowserFixture) *Application {
	t.Helper()
	base := filepath.Join(runDir, "application")
	logs := NewLogStore(32)
	proxy := &draftBrowserProxy{}
	t.Cleanup(func() {
		if n := proxy.calls.Load(); n != 0 {
			t.Errorf("browser fixture attempted %d proxy operations", n)
		}
	})
	app := &Application{
		baseDir: base, dataDir: filepath.Join(base, "data"), logsDir: filepath.Join(base, "logs"),
		config: &Settings{ConfigName: fixture.Profiles[0].Name, CloseToTray: true},
		logger: slog.New(logs), logStore: logs, events: NewEventHub(), proxy: proxy,
	}
	app.events.current = app.State
	seed := profileStore{ActiveID: fixture.Profiles[0].ID}
	for _, p := range fixture.Profiles {
		profile := Profile{
			ID: p.ID, Name: p.Name, Source: "local", Size: int64(len(p.YAML)),
			CreatedAt: "2026-11-20T00:00:00Z", UpdatedAt: "2026-11-20T00:00:00Z",
		}
		if p.Subscription != "" {
			profile.Source, profile.URL, profile.AutoUpdateHours = "subscription", p.Subscription, p.AutoHours
			if p.AutoHours > 0 {
				profile.NextUpdateAt = time.Now().UTC().Add(time.Duration(p.AutoHours) * time.Hour).Format(time.RFC3339Nano)
			}
		}
		if err := atomicWrite(app.profileContentPath(p.ID), []byte(p.YAML), 0o600); err != nil {
			t.Fatal(err)
		}
		seed.Profiles = append(seed.Profiles, profile)
	}
	for _, err := range []error{
		app.saveSettingsValue(*app.config),
		app.saveProfileStore(seed),
		app.syncCurrentSource(),
		app.saveCustomRules(context.Background(), []CustomRule{fixture.Rule}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return app
}

func installDraftBrowserValidator(t *testing.T, base string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	validator := filepath.Join(base, "runtime", coreExecutable)
	if err := copyFile(self, validator); err != nil {
		t.Fatal(err)
	}
	t.Setenv(draftBrowserValidator, validator)
}

func draftBrowserRoutes(app *Application, token string) http.Handler {
	routes := app.Routes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != app.config.Listen || r.Header.Get(draftBrowserFixtureHeader) != token {
			http.NotFound(w, r)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /__draft_browser_fixture":
			writeJSON(w, http.StatusOK, map[string]string{"token": token, "version": AppVersion})
		case "GET /api/settings":
			// The console polls this endpoint. The real handler reads HKCU.
			writeJSON(w, http.StatusOK, app.RuntimeSettings())
		case "GET /", "GET /favicon.ico", "GET /api/state", "GET /api/events", "GET /api/logs",
			"GET /api/core-logs", "GET /api/config", "GET /api/profiles", "GET /api/rules", "PUT /api/rules",
			"GET /api/backup", "POST /api/backup":
			routes.ServeHTTP(w, r)
		case "POST /api/profiles":
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
			var req struct{ Action string }
			if err != nil || json.Unmarshal(body, &req) != nil {
				http.Error(w, "invalid fixture profile request", http.StatusBadRequest)
				return
			}
			switch req.Action {
			case "switch", "edit", "rollback":
				r.Body = io.NopCloser(bytes.NewReader(body))
				routes.ServeHTTP(w, r)
			default:
				http.Error(w, "profile operation is not enabled in the browser fixture", http.StatusForbidden)
			}
		default:
			http.Error(w, "route is not enabled in the browser fixture", http.StatusForbidden)
		}
	})
}

func runDraftBrowserNode(t *testing.T, node, script, runDir string, fixture draftBrowserFixture) {
	t.Helper()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(), "NODE_OPTIONS=", "NODE_USE_ENV_PROXY=0", "NO_PROXY=*")
	cmd.SysProcAttr = hiddenProcessAttrs()
	logPath := filepath.Join(runDir, "browser.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		t.Fatal(err)
	}

	// Node waits for its manifest on stdin, so no browser can escape the Job
	// Object while the fixture is still assigning its process to that job.
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		windows.CloseHandle(process)
	}
	if err != nil {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("could not contain the browser process tree: %v", err)
	}
	writeErr := json.NewEncoder(stdin).Encode(fixture)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	if err := errors.Join(writeErr, closeErr, waitErr, ctx.Err()); err != nil {
		t.Fatalf("isolated Edge smoke failed: %v; read %s", err, logPath)
	}
}
