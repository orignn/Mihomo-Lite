//go:build windows

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Every artifact is fixture and isolated; no real launcher, kernel, user data,
// proxy, registry, private URL, or public network request is used by this suite.
func componentTestRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Clean(filepath.Join(wd, "../..", ".cache/tests/opt008-go"))
	if err = os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "run-")
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func componentTestManager(t *testing.T) *componentUpdateManager {
	t.Helper()
	m, err := newComponentUpdateManager(componentTestRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	m.guard = func(string) error { return nil }
	return m
}
func componentTestWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func componentTestPE(marker byte) []byte {
	data := make([]byte, 512)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[0x3c:], 0x80)
	copy(data[0x80:], []byte{'P', 'E', 0, 0})
	binary.LittleEndian.PutUint16(data[0x84:], 0x8664)
	binary.LittleEndian.PutUint16(data[0x94:], 240)
	binary.LittleEndian.PutUint16(data[0x96:], 2)
	binary.LittleEndian.PutUint16(data[0x98:], 0x20b)
	binary.LittleEndian.PutUint32(data[0x98+108:], 16)
	data[511] = marker
	return data
}

type componentZipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func componentTestZip(t *testing.T, entries ...componentZipEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		w, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func componentTestPayload(t *testing.T, c string) []byte {
	if c == "core" {
		return componentTestZip(t, componentZipEntry{"mihomo-windows-amd64-compatible-v1.2.3.exe", componentTestPE('N'), 0600})
	}
	return componentTestZip(t, componentZipEntry{"dist/index.html", []byte("<html>fixture new panel</html>"), 0600}, componentZipEntry{"dist/assets/app.js", []byte("window.fixtureFixture=true"), 0600})
}

type componentTestTransport struct {
	base  *url.URL
	inner http.RoundTripper
	calls atomic.Int64
}

func (t *componentTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
		return nil, errors.New("production URL escaped fixed API")
	}
	t.calls.Add(1)
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.base.Scheme
	u.Host = t.base.Host
	clone.URL = &u
	return t.inner.RoundTrip(clone)
}

type componentTestServer struct {
	transport  *componentTestTransport
	asset      componentAsset
	body       []byte
	metadata   map[string]any
	badHeaders atomic.Bool
	downloaded atomic.Int64
	handler    func(http.ResponseWriter, *http.Request)
}

func componentTestRemote(t *testing.T, m *componentUpdateManager, c string) *componentTestServer {
	t.Helper()
	body := componentTestPayload(t, c)
	sum := sha256.Sum256(body)
	asset := componentAsset{ID: 17, Name: componentExpectedAsset(c, "v1.2.3"), URL: fmt.Sprintf("https://api.github.com/repos/%s/releases/assets/17", componentRepository(c)), Size: int64(len(body)), Digest: "sha256:" + hex.EncodeToString(sum[:])}
	fixture := &componentTestServer{asset: asset, body: body, metadata: map[string]any{"tag_name": "v1.2.3", "draft": false, "prerelease": false, "assets": []componentAsset{asset}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "MihomoLite-Component-Updates" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			fixture.badHeaders.Store(true)
		}
		if strings.HasSuffix(r.URL.Path, "/latest") {
			if r.Header.Get("Accept") != "application/vnd.github+json" {
				fixture.badHeaders.Store(true)
			}
			json.NewEncoder(w).Encode(fixture.metadata)
			return
		}
		fixture.downloaded.Add(1)
		if r.Header.Get("Accept") != "application/octet-stream" {
			fixture.badHeaders.Store(true)
		}
		if fixture.handler != nil {
			fixture.handler(w, r)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(fixture.body)))
		w.Write(fixture.body)
	}))
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	fixture.transport = &componentTestTransport{base: base, inner: server.Client().Transport}
	m.client = &http.Client{Transport: fixture.transport, CheckRedirect: componentRedirect, Timeout: 10 * time.Second}
	return fixture
}
func componentTestPrepare(t *testing.T, m *componentUpdateManager, c string) {
	t.Helper()
	fixture := componentTestRemote(t, m, c)
	if err := m.check(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := m.download(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if fixture.badHeaders.Load() {
		t.Fatal("official request headers wrong")
	}
}
func componentTestSeed(t *testing.T, m *componentUpdateManager, c string) ([]componentFile, bool) {
	t.Helper()
	target := filepath.Join(m.root, filepath.FromSlash(componentTarget(c)))
	if c == "core" {
		componentTestWrite(t, target, componentTestPE('O'))
	} else {
		componentTestWrite(t, filepath.Join(target, "index.html"), []byte("old panel"))
		componentTestWrite(t, filepath.Join(target, "old.js"), []byte("old fixture script"))
	}
	componentTestWrite(t, filepath.Join(m.root, "data", "keep-user-data.txt"), []byte("fixture user sentinel"))
	files, exists, err := m.snapshot(c, target)
	if err != nil {
		t.Fatal(err)
	}
	return files, exists
}
func componentTestUnchanged(t *testing.T, m *componentUpdateManager, c string, files []componentFile, exists bool) {
	t.Helper()
	target := filepath.Join(m.root, filepath.FromSlash(componentTarget(c)))
	if err := m.verify(c, target, files, exists); err != nil {
		t.Fatal("old component changed:", err)
	}
	b, err := os.ReadFile(filepath.Join(m.root, "data", "keep-user-data.txt"))
	if err != nil || string(b) != "fixture user sentinel" {
		t.Fatal("unrelated user data changed")
	}
}
func TestComponentUpdateTrustedInstallRollback(t *testing.T) {
	for _, c := range []string{"core", "panel"} {
		t.Run(c, func(t *testing.T) {
			m := componentTestManager(t)
			before, exists := componentTestSeed(t, m, c)
			componentTestPrepare(t, m, c)
			prepared := m.prepared[c]
			if err := m.install(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(m.root, filepath.FromSlash(componentTarget(c)))
			if err := m.verify(c, target, prepared.Files, true); err != nil {
				t.Fatal(err)
			}
			tx := m.latest[c]
			if tx.State != "committed" || tx.ID == "" {
				t.Fatal("missing transaction")
			}
			disk, err := m.readTransaction(tx.ID)
			if err != nil || !componentFilesEqual(disk.Before, before) {
				t.Fatal("backup manifest failed", err)
			}
			old := filepath.Join(m.root, filepath.FromSlash(componentArchiveRoot), tx.ID, "old", "payload")
			if err = m.verify(c, old, before, exists); err != nil {
				t.Fatal(err)
			}
			if id := m.identity(c); id.Version != "v1.2.3" || id.IdentityStatus != "verified-update-manifest" {
				t.Fatal("wrong installed identity", id)
			}
			// New process reloads identities and rollback history without networking.
			fresh, err := newComponentUpdateManager(m.root)
			if err != nil {
				t.Fatal(err)
			}
			fresh.guard = func(string) error { return nil }
			if err = fresh.recover(); err != nil {
				t.Fatal(err)
			}
			if err = fresh.rollback(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			componentTestUnchanged(t, fresh, c, before, exists)
			if fresh.identity(c).Version != "" {
				t.Fatal("legacy backup fabricated remote version")
			}
			if fresh.latest[c].RollbackOf != tx.ID {
				t.Fatal("rollback is not journaled")
			}
		})
	}
}
func TestComponentUpdateTrustFailClosed(t *testing.T) {
	mutations := []struct {
		name   string
		change func(*componentTestServer)
	}{
		{"missing-digest", func(f *componentTestServer) { f.asset.Digest = "" }},
		{"short-digest", func(f *componentTestServer) { f.asset.Digest = "sha256:1234" }},
		{"sha512", func(f *componentTestServer) { f.asset.Digest = "sha512:" + strings.Repeat("a", 64) }},
		{"uppercase-digest", func(f *componentTestServer) { f.asset.Digest = "sha256:" + strings.Repeat("A", 64) }},
		{"arbitrary-url", func(f *componentTestServer) { f.asset.URL = "https://example.invalid/payload.zip" }},
		{"wrong-repository", func(f *componentTestServer) {
			f.asset.URL = "https://api.github.com/repos/evil/repository/releases/assets/17"
		}},
		{"http", func(f *componentTestServer) { f.asset.URL = strings.Replace(f.asset.URL, "https:", "http:", 1) }},
		{"url-query", func(f *componentTestServer) { f.asset.URL += "?redirect=x" }},
		{"url-user", func(f *componentTestServer) { f.asset.URL = strings.Replace(f.asset.URL, "//api.", "//user@api.", 1) }},
		{"wrong-id", func(f *componentTestServer) { f.asset.ID = 18 }},
		{"zero-size", func(f *componentTestServer) { f.asset.Size = 0 }},
		{"huge-size", func(f *componentTestServer) { f.asset.Size = 1 << 40 }},
		{"tag-traversal", func(f *componentTestServer) { f.metadata["tag_name"] = "../../payload" }},
		{"draft", func(f *componentTestServer) { f.metadata["draft"] = true }},
		{"prerelease", func(f *componentTestServer) { f.metadata["prerelease"] = true }},
	}
	for _, c := range []string{"core", "panel"} {
		for _, test := range mutations {
			t.Run(c+"/"+test.name, func(t *testing.T) {
				m := componentTestManager(t)
				before, exists := componentTestSeed(t, m, c)
				f := componentTestRemote(t, m, c)
				test.change(f)
				f.metadata["assets"] = []componentAsset{f.asset}
				if err := m.check(context.Background(), c); err == nil {
					t.Fatal("untrusted metadata accepted")
				}
				if err := m.download(context.Background(), c); err == nil {
					t.Fatal("download without trust accepted")
				}
				if f.downloaded.Load() != 0 {
					t.Fatal("untrusted asset downloaded")
				}
				componentTestUnchanged(t, m, c, before, exists)
			})
		}
	}
	t.Run("duplicate-asset", func(t *testing.T) {
		m := componentTestManager(t)
		f := componentTestRemote(t, m, "panel")
		f.metadata["assets"] = []componentAsset{f.asset, f.asset}
		if m.check(context.Background(), "panel") == nil {
			t.Fatal("duplicate accepted")
		}
	})
	t.Run("refresh-invalidates-selection", func(t *testing.T) {
		m := componentTestManager(t)
		f := componentTestRemote(t, m, "panel")
		if m.check(context.Background(), "panel") != nil {
			t.Fatal("initial check")
		}
		f.metadata["assets"] = []componentAsset{}
		if m.check(context.Background(), "panel") == nil {
			t.Fatal("invalid refresh accepted")
		}
		if m.views["panel"].Available != nil || m.download(context.Background(), "panel") == nil {
			t.Fatal("stale selection authorized")
		}
	})
}
func TestComponentUpdateDownloadValidation(t *testing.T) {
	for _, mode := range []string{"digest", "truncated", "oversized", "cancel", "invalid-pe"} {
		t.Run(mode, func(t *testing.T) {
			m := componentTestManager(t)
			before, exists := componentTestSeed(t, m, "core")
			f := componentTestRemote(t, m, "core")
			switch mode {
			case "digest":
				f.body = append([]byte(nil), f.body...)
				f.body[0] ^= 1
			case "truncated":
				f.body = f.body[:len(f.body)-1]
			case "oversized":
				f.body = append(f.body, 0)
			case "invalid-pe":
				f.body = componentTestZip(t, componentZipEntry{"mihomo.exe", []byte("not a PE"), 0600})
				h := sha256.Sum256(f.body)
				f.asset.Size = int64(len(f.body))
				f.asset.Digest = "sha256:" + hex.EncodeToString(h[:])
				f.metadata["assets"] = []componentAsset{f.asset}
			}
			if err := m.check(context.Background(), "core"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			if err := m.download(ctx, "core"); err == nil {
				t.Fatal("failed download accepted")
			}
			if _, ok := m.prepared["core"]; ok {
				t.Fatal("failed asset prepared")
			}
			componentTestUnchanged(t, m, "core", before, exists)
		})
	}
}
func TestComponentUpdateZipSafety(t *testing.T) {
	good := componentZipEntry{"dist/index.html", []byte("fixture"), 0600}
	tests := []struct {
		name    string
		entries []componentZipEntry
	}{
		{"parent", []componentZipEntry{good, {"../escape", []byte("x"), 0600}}},
		{"absolute", []componentZipEntry{good, {"/absolute", []byte("x"), 0600}}},
		{"drive", []componentZipEntry{good, {"C:/escape", []byte("x"), 0600}}},
		{"backslash", []componentZipEntry{good, {`dist\escape`, []byte("x"), 0600}}},
		{"symlink", []componentZipEntry{good, {"dist/link", []byte("../outside"), os.ModeSymlink | 0777}}},
		{"pipe", []componentZipEntry{good, {"dist/pipe", nil, os.ModeNamedPipe | 0600}}},
		{"case-alias", []componentZipEntry{good, {"dist/INDEX.html", []byte("x"), 0600}}},
		{"file-parent", []componentZipEntry{good, {"dist/assets", []byte("x"), 0600}, {"dist/assets/app.js", []byte("y"), 0600}}},
		{"directory-file-alias", []componentZipEntry{good, {"dist/assets/", nil, os.ModeDir | 0700}, {"dist/assets", []byte("x"), 0600}}},
		{"reserved", []componentZipEntry{good, {"dist/CON.txt", []byte("x"), 0600}}},
		{"trailing-dot", []componentZipEntry{good, {"dist/file.", []byte("x"), 0600}}},
		{"ambiguous-index", []componentZipEntry{good, {"dist/sub/index.html", []byte("x"), 0600}}},
		{"unrelated-root", []componentZipEntry{good, {"data/source.yaml", []byte("fixture"), 0600}}},
		{"zip-bomb", []componentZipEntry{good, {"dist/bomb.txt", bytes.Repeat([]byte("0"), 2<<20), 0600}}},
		{"empty-index", []componentZipEntry{{"dist/index.html", nil, 0600}}},
		{"too-deep", []componentZipEntry{good, {"dist/" + strings.Repeat("deep/", 21) + "file", []byte("x"), 0600}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := componentTestManager(t)
			before, exists := componentTestSeed(t, m, "panel")
			zipPath := filepath.Join(m.root, ".cache/downloads/test.zip")
			componentTestWrite(t, zipPath, componentTestZip(t, test.entries...))
			if _, err := m.extract("panel", zipPath, ".cache/downloads/test/payload"); err == nil {
				t.Fatal("unsafe zip accepted")
			}
			componentTestUnchanged(t, m, "panel", before, exists)
			if _, err := os.Stat(filepath.Join(m.root, "escape")); !os.IsNotExist(err) {
				t.Fatal("zip escaped")
			}
		})
	}
	t.Run("core-extra-payload", func(t *testing.T) {
		m := componentTestManager(t)
		p := filepath.Join(m.root, ".cache/downloads/core.zip")
		componentTestWrite(t, p, componentTestZip(t, componentZipEntry{"mihomo.exe", componentTestPE(0), 0600}, componentZipEntry{"extra.exe", componentTestPE(1), 0600}))
		if _, err := m.extract("core", p, ".cache/downloads/core/payload"); err == nil {
			t.Fatal("core extras accepted")
		}
	})
}
func TestComponentUpdateFailureRollbackAndInterruption(t *testing.T) {
	for _, c := range []string{"core", "panel"} {
		for _, point := range []string{"preparing", "prepared", "old-moved", "new-moved", "commit"} {
			for _, interruption := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/interruption=%v", c, point, interruption), func(t *testing.T) {
					m := componentTestManager(t)
					before, exists := componentTestSeed(t, m, c)
					componentTestPrepare(t, m, c)
					m.fault = func(p string) error {
						if p == point {
							if interruption {
								return errComponentInterrupted
							}
							return errors.New("fixture disk failure")
						}
						return nil
					}
					if err := m.install(context.Background(), c); err == nil {
						t.Fatal("failure ignored")
					}
					if !interruption {
						componentTestUnchanged(t, m, c, before, exists)
						if m.isBlocked() {
							t.Fatal("completed rollback left blocked")
						}
					}
					fresh, err := newComponentUpdateManager(m.root)
					if err != nil {
						t.Fatal(err)
					}
					fresh.guard = func(string) error { return nil }
					if err = fresh.recover(); err != nil {
						t.Fatal("startup recovery failed", err)
					}
					componentTestUnchanged(t, fresh, c, before, exists)
					if err = fresh.recover(); err != nil {
						t.Fatal("repeat recovery failed", err)
					}
					if fresh.isBlocked() {
						t.Fatal("recovered state blocked")
					}
				})
			}
		}
	}
}
func TestComponentUpdateCorruptBackupAndUnknownTargetFailClosed(t *testing.T) {
	for _, mode := range []string{"backup", "unknown-target", "manifest", "path-record", "running"} {
		t.Run(mode, func(t *testing.T) {
			m := componentTestManager(t)
			componentTestSeed(t, m, "panel")
			componentTestPrepare(t, m, "panel")
			m.fault = func(p string) error {
				if p == "new-moved" {
					return errComponentInterrupted
				}
				return nil
			}
			if m.install(context.Background(), "panel") == nil {
				t.Fatal("no interruption")
			}
			root := filepath.Join(m.root, filepath.FromSlash(componentArchiveRoot))
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			var id string
			for _, entry := range entries {
				if entry.IsDir() {
					id = entry.Name()
				}
			}
			tx, err := m.readTransaction(id)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "backup":
				componentTestWrite(t, filepath.Join(root, id, "old/payload/index.html"), []byte("corrupt backup"))
			case "unknown-target":
				componentTestWrite(t, filepath.Join(m.root, "data/ui/index.html"), []byte("foreign edit"))
			case "manifest":
				componentTestWrite(t, filepath.Join(root, id, "manifest.json"), []byte("corrupt journal"))
			case "path-record":
				tx.Before[0].Path = "../../source.yaml"
				if err = m.writeTransaction(tx); err != nil {
					t.Fatal(err)
				}
			}
			targetBefore, _, err := m.snapshot("panel", filepath.Join(m.root, "data/ui"))
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := newComponentUpdateManager(m.root)
			if err != nil {
				t.Fatal(err)
			}
			fresh.guard = func(string) error {
				if mode == "running" {
					return errComponentStopped
				}
				return nil
			}
			if err = fresh.recover(); err == nil || !fresh.isBlocked() {
				t.Fatal("unsafe recovery authorized")
			}
			if err = fresh.verify("panel", filepath.Join(m.root, "data/ui"), targetBefore, true); err != nil {
				t.Fatal("unsafe recovery changed target")
			}
		})
	}
}
func TestComponentUpdateStoppedAndControlledPolicy(t *testing.T) {
	for _, mode := range []string{"running", "guard-race", "controlled-current", "corrupt-current", "release-only", "tampered-download", "tampered-stage"} {
		t.Run(mode, func(t *testing.T) {
			m := componentTestManager(t)
			before, exists := componentTestSeed(t, m, "core")
			componentTestPrepare(t, m, "core")
			switch mode {
			case "running":
				m.guard = func(string) error { return errComponentStopped }
			case "guard-race":
				var calls int
				m.guard = func(string) error {
					calls++
					if calls >= 3 {
						return errComponentStopped
					}
					return nil
				}
			case "controlled-current":
				componentTestWrite(t, filepath.Join(m.root, "CURRENT.json"), []byte(`{"schemaVersion":1,"binary":{"path":"MihomoLite.exe","sha256":"`+strings.Repeat("a", 64)+`","size":1},"release":{"manifest":"release/current.json"}}`))
			case "corrupt-current":
				componentTestWrite(t, filepath.Join(m.root, "CURRENT.json"), []byte("{}"))
			case "release-only":
				componentTestWrite(t, filepath.Join(m.root, "release/current.json"), []byte("{}"))
			case "tampered-download":
				p := m.prepared["core"]
				componentTestWrite(t, filepath.Join(m.root, filepath.FromSlash(componentDownloadRoot), p.ID, "package.zip"), []byte("tampered"))
			case "tampered-stage":
				p := m.prepared["core"]
				componentTestWrite(t, filepath.Join(m.root, filepath.FromSlash(componentDownloadRoot), p.ID, "payload"), componentTestPE('X'))
			}
			if m.install(context.Background(), "core") == nil {
				t.Fatal("unsafe install accepted")
			}
			componentTestUnchanged(t, m, "core", before, exists)
		})
	}
}
func TestComponentUpdatePathsAndNativeLocks(t *testing.T) {
	m := componentTestManager(t)
	for _, path := range []string{"../escape", "/absolute", "C:/escape", `data\ui`, "data/ui/../source.yaml", "data/CON.txt", "data/file ", "data/file.", "data/x:y", "data/\x00"} {
		if _, err := m.path(path); err == nil {
			t.Fatal("unsafe path accepted", path)
		}
	}
	t.Run("directory-junction", func(t *testing.T) {
		outside := componentTestRoot(t)
		componentTestWrite(t, filepath.Join(outside, "sentinel"), []byte("outside"))
		link := filepath.Join(m.root, ".cache/downloads/link")
		if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
			t.Fatal(err)
		}
		// A junction needs no symlink privilege and is created only between
		// these absolute isolated fixture directories, never a runtime path.
		if !filepath.IsAbs(link) || !strings.HasPrefix(link, m.root+string(filepath.Separator)) || !filepath.IsAbs(outside) {
			t.Fatal("unverified junction paths")
		}
		if err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside).Run(); err != nil {
			t.Fatal("fixture junction creation failed", err)
		}
		if _, err := m.path(".cache/downloads/link/sentinel"); err == nil {
			t.Fatal("reparse path accepted")
		}
	})
	t.Run("exclusive-update-lock", func(t *testing.T) {
		root, err := m.mkdir(componentArchiveRoot)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(root, "update.lock")
		unlock, err := componentAcquireFileLock(p)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if release, e := componentAcquireFileLock(p); e == nil {
			release()
			t.Fatal("second writer obtained lock")
		}
	})
	t.Run("locked-executable-no-overwrite", func(t *testing.T) {
		n := componentTestManager(t)
		before, exists := componentTestSeed(t, n, "core")
		componentTestPrepare(t, n, "core")
		p, _ := windows.UTF16PtrFromString(filepath.Join(n.root, "runtime/mihomo.exe"))
		h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(h)
		if n.install(context.Background(), "core") == nil {
			t.Fatal("locked executable overwritten")
		}
		componentTestUnchanged(t, n, "core", before, exists)
	})
}
func componentTestApplication(t *testing.T) *Application {
	t.Helper()
	m := componentTestManager(t)
	logs := NewLogStore(10)
	a := &Application{baseDir: m.root, dataDir: filepath.Join(m.root, "data"), logsDir: filepath.Join(m.root, "logs"), config: &Settings{Listen: "127.0.0.1:17653", CloseToTray: true}, logger: slog.New(logs), logStore: logs, events: NewEventHub(), proxy: httpTestProxy{}, componentUpdates: m}
	a.events.current = a.State
	return a
}
func TestComponentUpdateHTTPAndLauncherUnconfigured(t *testing.T) {
	a := componentTestApplication(t)
	m := a.componentUpdates
	fixture := componentTestRemote(t, m, "panel")
	// Production route auth wrapper must reject cross-origin and missing marker.
	for _, marker := range []string{"", "0"} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17653/api/components", strings.NewReader(`{"action":"check","component":"panel"}`))
		request.Header.Set("X-Mihomo-Lite", marker)
		rec := httptest.NewRecorder()
		a.Routes().ServeHTTP(rec, request)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("missing auth: %d", rec.Code)
		}
	}
	requests := []string{
		`{"action":"install","component":"panel"}`,
		`{"action":"download","component":"evil"}`,
		`{"action":"check","component":"panel","url":"https://example.invalid"}`,
		`{"action":"check","component":"panel","path":"../outside"}`,
		`{"action":"check","component":"panel"} {}`,
		`{"action":"evil","component":"panel"}`,
	}
	for _, body := range requests {
		rec := httptest.NewRecorder()
		a.componentsHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/components", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("unsafe request accepted: %d", rec.Code)
		}
	}
	if fixture.transport.calls.Load() != 0 {
		t.Fatal("bad request caused network")
	}
	rec := httptest.NewRecorder()
	a.componentsHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/components", strings.NewReader(`{"action":"check","component":"launcher"}`)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "unconfigured") {
		t.Fatal("launcher fabricated trust", rec.Code, rec.Body.String())
	}
	if fixture.transport.calls.Load() != 0 {
		t.Fatal("unconfigured launcher contacted remote")
	}
	rec = httptest.NewRecorder()
	a.componentsHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/components", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"trustStatus":"unconfigured"`) {
		t.Fatal("launcher status missing")
	}
	// GET is always local, even if called repeatedly during disconnected usage.
	if fixture.transport.calls.Load() != 0 {
		t.Fatal("GET checked remotely")
	}
	m.setBlocked(true)
	if !errors.Is(a.checkComponentUpdateRecovery(), errComponentRecovery) {
		t.Fatal("startup guard did not block")
	}
}
func TestComponentUpdateLocalManifestIdentity(t *testing.T) {
	m := componentTestManager(t)
	binary := componentTestPE('L')
	componentTestWrite(t, filepath.Join(m.root, "MihomoLite.exe"), binary)
	sum := sha256.Sum256(binary)
	file := componentFile{Path: "MihomoLite.exe", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(binary))}
	current := map[string]any{"schemaVersion": 1, "version": "0.6.15", "binary": file, "release": map[string]any{"manifest": "release/current.json"}}
	b, _ := json.Marshal(current)
	componentTestWrite(t, filepath.Join(m.root, "CURRENT.json"), b)
	if identity := m.identity("launcher"); identity.ManifestStatus != "current-match" || identity.Version != "0.6.15" {
		t.Fatal("CURRENT identity wrong", identity)
	}
	file.Path = "MihomoLite/MihomoLite.exe"
	release := map[string]any{"schemaVersion": 1, "version": "0.6.15", "binary": file, "files": []componentFile{file}}
	b, _ = json.Marshal(release)
	componentTestWrite(t, filepath.Join(m.root, "release/current.json"), b)
	if m.identity("launcher").ManifestStatus != "controlled-release-match" {
		t.Fatal("release identity missing")
	}
	componentTestWrite(t, filepath.Join(m.root, "MihomoLite.exe"), componentTestPE('X'))
	if m.identity("launcher").ManifestStatus != "controlled-release-mismatch" {
		t.Fatal("mismatch hidden")
	}
	if m.installationPolicy() != errComponentControlled {
		t.Fatal("controlled release can be mutated")
	}
}
func TestComponentUpdateRedirectRestrictions(t *testing.T) {
	original, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/MetaCubeX/mihomo/releases/assets/17", nil)
	for _, target := range []string{"http://release-assets.githubusercontent.com/a", "https://example.invalid/a", "https://github.com/evil/a", "https://api.github.com:443/a", "https://user@objects.githubusercontent.com/a", "https://objects.githubusercontent.com/a#fragment"} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if componentRedirect(req, []*http.Request{original}) == nil {
			t.Fatal("unsafe redirect", target)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/public?sig=fixture", nil)
	req.Header.Set("Authorization", "never-forward")
	if err := componentRedirect(req, []*http.Request{original}); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("authorization forwarded")
	}
	latest, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/MetaCubeX/mihomo/releases/latest", nil)
	if componentRedirect(req, []*http.Request{latest}) == nil {
		t.Fatal("metadata redirected")
	}
}
func TestComponentUpdateProgressAndConcurrentOperation(t *testing.T) {
	m := componentTestManager(t)
	fixture := componentTestRemote(t, m, "panel")
	if err := m.check(context.Background(), "panel"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	fixture.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(fixture.body)))
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Write(fixture.body)
	}
	done := make(chan error, 1)
	go func() { done <- m.download(context.Background(), "panel") }()
	<-started
	if err := m.check(context.Background(), "core"); !errors.Is(err, errComponentBusy) {
		t.Fatal("concurrent operation accepted", err)
	}
	// Status has a separate lock and does not wait for the downloading operation.
	data, err := json.Marshal(m.status(true))
	if err != nil || !bytes.Contains(data, []byte(`"phase":"downloading"`)) {
		t.Fatal("progress unavailable", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if m.views["panel"].Progress.Phase != "ready" || m.views["panel"].Progress.Downloaded != fixture.asset.Size {
		t.Fatal("final progress wrong")
	}
}

func TestComponentUpdateRecoveryAfterRollbackWriteFailure(t *testing.T) {
	m := componentTestManager(t)
	before, exists := componentTestSeed(t, m, "core")
	componentTestPrepare(t, m, "core")
	var locked windows.Handle
	m.fault = func(point string) error {
		if point != "new-moved" {
			return nil
		}
		entries, err := os.ReadDir(filepath.Join(m.root, filepath.FromSlash(componentArchiveRoot)))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				p, e := windows.UTF16PtrFromString(filepath.Join(m.root, filepath.FromSlash(componentArchiveRoot), entry.Name(), "old/payload"))
				if e != nil {
					return e
				}
				locked, e = windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
				if e != nil {
					return e
				}
			}
		}
		return errors.New("fixture disk failure; old executable locked during rollback")
	}
	if err := m.install(context.Background(), "core"); !errors.Is(err, errComponentRecovery) || !m.isBlocked() {
		t.Fatal("rollback write failure did not block", err)
	}
	if locked == 0 {
		t.Fatal("rollback failure fixture did not lock old payload")
	}
	windows.CloseHandle(locked)
	fresh, err := newComponentUpdateManager(m.root)
	if err != nil {
		t.Fatal(err)
	}
	fresh.guard = func(string) error { return nil }
	if err = fresh.recover(); err != nil {
		t.Fatal("rollback retry failed", err)
	}
	componentTestUnchanged(t, fresh, "core", before, exists)
	if err = fresh.recover(); err != nil {
		t.Fatal("idempotent retry failed", err)
	}
}
func TestComponentUpdateInstallCancellationAfterSwitch(t *testing.T) {
	for _, c := range []string{"core", "panel"} {
		t.Run(c, func(t *testing.T) {
			m := componentTestManager(t)
			before, exists := componentTestSeed(t, m, c)
			componentTestPrepare(t, m, c)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.fault = func(point string) error {
				if point == "new-moved" {
					cancel()
				}
				return nil
			}
			if m.install(ctx, c) == nil {
				t.Fatal("canceled install committed")
			}
			componentTestUnchanged(t, m, c, before, exists)
			if m.isBlocked() {
				t.Fatal("cancellation rollback blocked")
			}
		})
	}
}
func TestComponentUpdateZipHeaderBoundsAndCRC(t *testing.T) {
	for _, mode := range []string{"declared-huge", "too-many", "bad-crc", "core-wrong-machine"} {
		t.Run(mode, func(t *testing.T) {
			m := componentTestManager(t)
			c := "panel"
			var data []byte
			switch mode {
			case "declared-huge":
				data = componentTestPayload(t, "panel")
				offset := bytes.Index(data, []byte{'P', 'K', 1, 2})
				if offset < 0 {
					t.Fatal("fixture central directory")
				}
				binary.LittleEndian.PutUint32(data[offset+24:], 0x7fffffff)
			case "too-many":
				entries := []componentZipEntry{{"dist/index.html", []byte("fixture"), 0600}}
				for i := 0; i < 8192; i++ {
					entries = append(entries, componentZipEntry{fmt.Sprintf("dist/files/%04d", i), nil, 0600})
				}
				data = componentTestZip(t, entries...)
			case "bad-crc":
				data = componentTestPayload(t, "panel")
				offset := bytes.Index(data, []byte{'P', 'K', 1, 2})
				if offset < 0 {
					t.Fatal("fixture central directory")
				}
				data[offset+16] ^= 1
			case "core-wrong-machine":
				c = "core"
				pe := componentTestPE(0)
				binary.LittleEndian.PutUint16(pe[0x84:], 0x14c)
				data = componentTestZip(t, componentZipEntry{"mihomo.exe", pe, 0600})
			}
			path := filepath.Join(m.root, ".cache/downloads/bounds.zip")
			componentTestWrite(t, path, data)
			if _, err := m.extract(c, path, ".cache/downloads/bounds/payload"); err == nil {
				t.Fatal("unsafe archive bounds accepted")
			}
		})
	}
}
func TestComponentUpdateHTTPExplicitInstallDisconnectRollback(t *testing.T) {
	a := componentTestApplication(t)
	m := a.componentUpdates
	before, exists := componentTestSeed(t, m, "panel")
	fixture := componentTestRemote(t, m, "panel")
	request := func(action string, confirm bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"action": action, "component": "panel", "confirm": confirm})
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17653/api/components", bytes.NewReader(body))
		r.Header.Set("X-Mihomo-Lite", "1")
		r.Header.Set("Origin", "http://127.0.0.1:17653")
		w := httptest.NewRecorder()
		a.Routes().ServeHTTP(w, r)
		return w
	}
	for _, action := range []string{"check", "download"} {
		if rec := request(action, false); rec.Code != http.StatusOK {
			t.Fatal("authorized prepare failed", action, rec.Code, rec.Body.String())
		}
	}
	a.mu.Lock()
	a.corePID = 123
	a.mu.Unlock()
	if rec := request("install", true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "断开") {
		t.Fatal("running install not refused", rec.Code, rec.Body.String())
	}
	componentTestUnchanged(t, m, "panel", before, exists)
	a.mu.Lock()
	a.corePID = 0
	a.mu.Unlock()
	if rec := request("install", true); rec.Code != http.StatusOK {
		t.Fatal("confirmed stopped install failed", rec.Code, rec.Body.String())
	}
	if rec := request("rollback", true); rec.Code != http.StatusOK {
		t.Fatal("confirmed rollback failed", rec.Code, rec.Body.String())
	}
	componentTestUnchanged(t, m, "panel", before, exists)
	if fixture.transport.calls.Load() != 2 {
		t.Fatal("installation or rollback made remote requests")
	}
}

func TestComponentUpdateExclusiveSlotsAndNoReplace(t *testing.T) {
	m := componentTestManager(t)
	if _, err := m.mkdir("archive/component-updates"); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("1", 32)
	relative := componentArchiveRoot + "/" + id
	if _, err := m.mkdirExclusive(relative); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(m.root, filepath.FromSlash(relative), "manifest.json")
	componentTestWrite(t, marker, []byte("existing archive sentinel"))
	if _, err := m.mkdirExclusive(relative); err == nil {
		t.Fatal("existing slot reused")
	}
	source := filepath.Join(m.root, ".cache/downloads/source")
	destination := filepath.Join(m.root, ".cache/downloads/destination")
	componentTestWrite(t, source, []byte("new source"))
	componentTestWrite(t, destination, []byte("existing target sentinel"))
	if componentRenameNoReplace(source, destination) == nil {
		t.Fatal("native move overwrote existing target")
	}
	for _, test := range []struct{ path, expected string }{{marker, "existing archive sentinel"}, {source, "new source"}, {destination, "existing target sentinel"}} {
		b, err := os.ReadFile(test.path)
		if err != nil || string(b) != test.expected {
			t.Fatal("safe slot/move changed existing identity", err)
		}
	}
}
func TestComponentUpdateJSONExactAndDuplicates(t *testing.T) {
	a := componentTestApplication(t)
	fixture := componentTestRemote(t, a.componentUpdates, "panel")
	for _, body := range []string{
		`{"action":"check","action":"download","component":"panel"}`,
		`{"action":"check","Action":"download","component":"panel"}`,
		`{"Action":"check","component":"panel"}`,
		`{"action":"check","Component":"panel"}`,
		`{"action":"check","component":"panel","confirm":null}`,
		`{"action":"install","component":"panel","confirm":false,"confirm":true}`,
		`{"action":"check","component":"panel","component":"core"}`,
	} {
		rec := httptest.NewRecorder()
		a.componentsHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/components", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatal("ambiguous JSON request accepted", rec.Code)
		}
	}
	if fixture.transport.calls.Load() != 0 {
		t.Fatal("ambiguous input reached remote")
	}
	type schema struct {
		ID   string        `json:"id"`
		File componentFile `json:"file"`
	}
	for _, body := range []string{
		`{"id":"a","ID":"b","file":{"path":"x","sha256":"x","size":1}}`,
		`{"ID":"a","file":{"path":"x","sha256":"x","size":1}}`,
		`{"id":"a","file":{"path":"x","sha256":"x","SHA256":"y","size":1}}`,
		`{"id":"a","file":{"path":"x","sha256":"x","size":1},"url":"https://example.invalid"}`,
		`{"id":"a","file":{"path":"x","sha256":"x"}}`,
	} {
		var value schema
		if componentJSONExact([]byte(body), &value) == nil {
			t.Fatal("ambiguous or unknown manifest JSON accepted")
		}
	}
	// Upstream unknown fields are allowed, but duplicate trust metadata is not.
	if componentJSONUnique([]byte(`{"tag_name":"v1.2.3","future":{"valid":true}}`)) != nil {
		t.Fatal("official additional fields rejected")
	}
	if componentJSONUnique([]byte(`{"assets":[{"digest":"sha256:one","Digest":"sha256:two"}]}`)) == nil {
		t.Fatal("duplicate digest accepted")
	}
}
