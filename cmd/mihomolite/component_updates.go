package main

// OPT-008: remote trust is deliberately compiled in. Local manifests are identity
// evidence, never an authorization to download arbitrary URLs or execute helpers.
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	errComponentTrust        = errors.New("组件来源或 SHA-256 摘要不可信，已拒绝更新")
	errComponentStopped      = errors.New("请先明确断开连接并等待内核退出，再安装或回滚组件；不会自动停机或强杀")
	errComponentControlled   = errors.New("受控发布安装必须通过整包受控发布，不能单独更新组件改变发布载荷")
	errComponentRecovery     = errors.New("组件更新恢复未完成，已保留安全更新槽；请退出后重新启动恢复，暂不允许连接或更新")
	errComponentBusy         = errors.New("另一个组件操作正在进行，请稍候")
	errComponentUnconfigured = errors.New("启动器可信发布仓库及签名信任尚未配置（unconfigured）；只支持本地身份检查，不提供远端下载或自替换")
	componentTagRE           = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9][A-Za-z0-9.-]{0,63})?$`)
	componentIDRE            = regexp.MustCompile("^[0-9a-f]{32}$")
	componentSHA256RE        = regexp.MustCompile("^[0-9a-f]{64}$")
)

const componentArchiveRoot = "archive/component-updates"
const componentDownloadRoot = ".cache/downloads/component-updates"
const componentMaxJSON = 4 << 20

type componentFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type componentIdentity struct {
	Version        string `json:"version"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	IdentityStatus string `json:"identityStatus"`
	ManifestStatus string `json:"manifestStatus"`
}
type componentAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}
type componentRelease struct {
	Component  string         `json:"component"`
	Repository string         `json:"repository"`
	Version    string         `json:"version"`
	Asset      componentAsset `json:"asset"`
}
type componentProgress struct {
	Phase      string `json:"phase"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
}
type componentView struct {
	Component          string            `json:"component"`
	Current            componentIdentity `json:"current"`
	TrustStatus        string            `json:"trustStatus"`
	Repository         string            `json:"repository"`
	CheckedAt          string            `json:"checkedAt"`
	Available          *componentRelease `json:"available"`
	Progress           componentProgress `json:"progress"`
	CanInstall         bool              `json:"canInstall"`
	CanRollback        bool              `json:"canRollback"`
	RequiresDisconnect bool              `json:"requiresDisconnect"`
	InstallBlockReason string            `json:"installBlockReason,omitempty"`
	RollbackID         string            `json:"rollbackID,omitempty"`
}
type componentDownload struct {
	ID      string           `json:"id"`
	Release componentRelease `json:"release"`
	Files   []componentFile  `json:"files"`
}
type componentUpdateManager struct {
	root     string
	client   *http.Client
	opMu     sync.Mutex
	mu       sync.Mutex
	views    map[string]componentView
	prepared map[string]componentDownload
	latest   map[string]componentTransaction
	blocked  bool
	// Only package tests inject transports/guards/faults. There is no API/config URL override.
	guard func(string) error
	fault func(string) error
}

func componentRepository(component string) string {
	switch component {
	case "core":
		return "MetaCubeX/mihomo"
	case "panel":
		return "Zephyruso/zashboard"
	}
	return ""
}
func componentTarget(component string) string {
	switch component {
	case "core":
		return "runtime/mihomo.exe"
	case "panel":
		return "data/ui"
	}
	return ""
}
func componentMaxArchive(component string) int64 {
	if component == "core" {
		return 128 << 20
	}
	return 64 << 20
}
func componentMaxExpanded(component string) int64 {
	if component == "core" {
		return 256 << 20
	}
	return 128 << 20
}
func componentExpectedAsset(component, version string) string {
	if component == "core" {
		return "mihomo-windows-amd64-compatible-" + version + ".zip"
	}
	if component == "panel" {
		return "dist-no-fonts.zip"
	}
	return ""
}
func newComponentUpdateManager(root string) (*componentUpdateManager, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, errComponentTrust
	}
	if err = componentCheckPath(absolute); err != nil {
		return nil, err
	}
	st, err := os.Stat(absolute)
	if err != nil || !st.IsDir() {
		return nil, errComponentTrust
	}
	transport := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	m := &componentUpdateManager{root: absolute, client: &http.Client{Transport: transport, Timeout: 15 * time.Minute, CheckRedirect: componentRedirect}, views: map[string]componentView{}, prepared: map[string]componentDownload{}, latest: map[string]componentTransaction{}}
	m.guard = func(component string) error { return componentCoreStopped(absolute) }
	for _, c := range []string{"launcher", "core", "panel"} {
		trust := "configured"
		if c == "launcher" {
			trust = "unconfigured"
		}
		m.views[c] = componentView{Component: c, TrustStatus: trust, Repository: componentRepository(c), Progress: componentProgress{Phase: "idle"}}
	}
	return m, nil
}
func componentRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Port() != "" || req.URL.Fragment != "" {
		return errComponentTrust
	}
	// API assets redirect to GitHub's fixed public release object services only.
	switch req.URL.Host {
	case "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
	default:
		return errComponentTrust
	}
	if len(via) == 0 || via[0].URL.Host != "api.github.com" || !strings.Contains(via[0].URL.Path, "/releases/assets/") {
		return errComponentTrust
	}
	req.Header.Del("Authorization")
	return nil
}
func (m *componentUpdateManager) path(relative string) (string, error) {
	if !componentSafeRelative(relative) {
		return "", errComponentTrust
	}
	full := filepath.Join(m.root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(m.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errComponentTrust
	}
	if err = componentCheckPath(full); err != nil {
		return "", err
	}
	return full, nil
}
func componentSafeRelative(path string) bool {
	if path == "" || len(path) > 1024 || strings.ContainsAny(path, "\\:\x00") || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) > 20 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 240 || strings.ContainsAny(part, "<>\"|?*") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		for _, r := range part {
			if r < 32 {
				return false
			}
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return false
		}
	}
	return true
}
func (m *componentUpdateManager) mkdir(relative string) (string, error) {
	p, err := m.path(relative)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(p, 0700); err != nil {
		return "", err
	}
	if err = componentCheckPath(p); err != nil {
		return "", err
	}
	return p, nil
}
func (m *componentUpdateManager) mkdirExclusive(relative string) (string, error) {
	p, err := m.path(relative)
	if err != nil {
		return "", err
	}
	if err = os.Mkdir(p, 0700); err != nil {
		return "", err
	}
	if err = componentCheckPath(p); err != nil {
		return "", err
	}
	return p, nil
}
func componentRandomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func componentHash(path string) (string, int64, error) {
	if err := componentCheckPath(path); err != nil {
		return "", 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() < 0 || st.Size() > 256<<20 {
		return "", 0, errComponentTrust
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || n != st.Size() {
		return "", 0, errComponentTrust
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func componentTreeIdentity(files []componentFile) (string, int64) {
	if len(files) == 1 && files[0].Path == "mihomo.exe" {
		return files[0].SHA256, files[0].Size
	}
	b, _ := json.Marshal(files)
	h := sha256.Sum256(b)
	var size int64
	for _, f := range files {
		size += f.Size
	}
	return hex.EncodeToString(h[:]), size
}
func componentReadJSON(path string, value any) error {
	return componentReadJSONMode(path, value, false)
}
func componentReadJSONStrict(path string, value any) error {
	return componentReadJSONMode(path, value, true)
}
func componentReadJSONMode(path string, value any, strict bool) error {
	if err := componentCheckPath(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > componentMaxJSON {
		return errComponentTrust
	}
	data, err := io.ReadAll(io.LimitReader(f, componentMaxJSON+1))
	if err != nil || len(data) > componentMaxJSON {
		return errComponentTrust
	}
	if strict {
		return componentJSONExact(data, value)
	}
	if componentJSONUnique(data) != nil || json.Unmarshal(data, value) != nil {
		return errComponentTrust
	}
	return nil
}
func componentWriteJSON(path string, value any) error {
	if err := componentCheckPath(path); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".journal-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	// No user data here: only this writer's exclusively created temporary journal.
	defer os.Remove(name)
	if _, err = temp.Write(append(b, '\n')); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = componentCheckPath(path); err != nil {
		return err
	}
	return replaceFile(name, path)
}
func (m *componentUpdateManager) setProgress(c, phase string, n, total int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.views[c]
	v.Progress = componentProgress{Phase: phase, Downloaded: n, Total: total}
	if err != nil {
		v.Progress.Error = err.Error()
	}
	m.views[c] = v
}
func (m *componentUpdateManager) isBlocked() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.blocked }
func (m *componentUpdateManager) setBlocked(blocked bool) {
	m.mu.Lock()
	m.blocked = blocked
	m.mu.Unlock()
}
func (m *componentUpdateManager) get(ctx context.Context, endpoint, accept string) (*http.Response, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errComponentTrust
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errComponentTrust
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "MihomoLite-Component-Updates")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, errors.New("官方组件请求失败或已取消")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("官方组件接口返回 HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func (m *componentUpdateManager) check(ctx context.Context, c string) (err error) {
	if !m.opMu.TryLock() {
		return errComponentBusy
	}
	defer m.opMu.Unlock()
	if c == "launcher" {
		m.setProgress(c, "unconfigured", 0, 0, errComponentUnconfigured)
		return errComponentUnconfigured
	}
	repo := componentRepository(c)
	if repo == "" {
		return errComponentTrust
	}
	if m.isBlocked() {
		return errComponentRecovery
	}
	m.setProgress(c, "checking", 0, 0, nil)
	// Invalidate old selection before any network request: a failed refresh cannot
	// leave an apparently current install authorization in the UI.
	m.mu.Lock()
	v := m.views[c]
	v.Available = nil
	v.CheckedAt = ""
	m.views[c] = v
	delete(m.prepared, c)
	m.mu.Unlock()
	defer func() {
		if err != nil {
			m.setProgress(c, "failed", 0, 0, err)
		}
	}()
	resp, err := m.get(ctx, "https://api.github.com/repos/"+repo+"/releases/latest", "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		TagName    string           `json:"tag_name"`
		Draft      bool             `json:"draft"`
		Prerelease bool             `json:"prerelease"`
		Assets     []componentAsset `json:"assets"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, componentMaxJSON+1))
	if err != nil || len(data) > componentMaxJSON {
		return errComponentTrust
	}
	if componentJSONUnique(data) != nil || json.Unmarshal(data, &result) != nil || !componentTagRE.MatchString(result.TagName) || result.Draft || result.Prerelease {
		return errComponentTrust
	}
	name := componentExpectedAsset(c, result.TagName)
	var found *componentAsset
	for i := range result.Assets {
		if result.Assets[i].Name == name {
			if found != nil {
				return errComponentTrust
			}
			found = &result.Assets[i]
		}
	}
	if found == nil {
		return errors.New("官方发布没有适配的组件资产")
	}
	release := componentRelease{Component: c, Repository: repo, Version: result.TagName, Asset: *found}
	if err = componentValidateRelease(release); err != nil {
		return err
	}
	m.mu.Lock()
	v = m.views[c]
	v.Available = &release
	v.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	m.views[c] = v
	m.mu.Unlock()
	m.setProgress(c, "checked", 0, found.Size, nil)
	return nil
}
func componentValidateRelease(r componentRelease) error {
	if componentRepository(r.Component) == "" || r.Repository != componentRepository(r.Component) || !componentTagRE.MatchString(r.Version) || r.Asset.Name != componentExpectedAsset(r.Component, r.Version) || r.Asset.ID <= 0 || r.Asset.Size <= 0 || r.Asset.Size > componentMaxArchive(r.Component) {
		return errComponentTrust
	}
	if r.Asset.URL != fmt.Sprintf("https://api.github.com/repos/%s/releases/assets/%d", r.Repository, r.Asset.ID) {
		return errComponentTrust
	}
	if !strings.HasPrefix(r.Asset.Digest, "sha256:") || !componentSHA256RE.MatchString(strings.TrimPrefix(r.Asset.Digest, "sha256:")) {
		return errComponentTrust
	}
	return nil
}

type componentProgressWriter struct {
	m        *componentUpdateManager
	c        string
	n, total int64
}

func (w *componentProgressWriter) Write(b []byte) (int, error) {
	w.n += int64(len(b))
	w.m.setProgress(w.c, "downloading", w.n, w.total, nil)
	return len(b), nil
}
func (m *componentUpdateManager) download(ctx context.Context, c string) (err error) {
	if !m.opMu.TryLock() {
		return errComponentBusy
	}
	defer m.opMu.Unlock()
	if c == "launcher" {
		return errComponentUnconfigured
	}
	if componentRepository(c) == "" {
		return errComponentTrust
	}
	if m.isBlocked() {
		return errComponentRecovery
	}
	m.mu.Lock()
	v := m.views[c]
	delete(m.prepared, c)
	m.mu.Unlock()
	if v.Available == nil {
		return errors.New("请先显式检查官方版本")
	}
	release := *v.Available
	if err = componentValidateRelease(release); err != nil {
		return err
	}
	var progress componentProgressWriter
	progress.m = m
	progress.c = c
	progress.total = release.Asset.Size
	defer func() {
		if err != nil {
			m.setProgress(c, "failed", progress.n, progress.total, err)
		}
	}()
	id, err := componentRandomID()
	if err != nil {
		return err
	}
	relative := componentDownloadRoot + "/" + id
	if _, err = m.mkdir(componentDownloadRoot); err != nil {
		return err
	}
	dir, err := m.mkdirExclusive(relative)
	if err != nil {
		return err
	}
	packagePath, err := m.path(relative + "/package.zip")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(packagePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	m.setProgress(c, "downloading", 0, release.Asset.Size, nil)
	resp, err := m.get(ctx, release.Asset.URL, "application/octet-stream")
	if err != nil {
		f.Close()
		return err
	}
	if resp.ContentLength >= 0 && resp.ContentLength != release.Asset.Size {
		resp.Body.Close()
		f.Close()
		return errors.New("组件下载大小与官方资产记录不一致")
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h, &progress), io.LimitReader(resp.Body, release.Asset.Size+1))
	resp.Body.Close()
	if copyErr == nil {
		copyErr = f.Sync()
	}
	if e := f.Close(); copyErr == nil {
		copyErr = e
	}
	if copyErr != nil || n != release.Asset.Size || hex.EncodeToString(h.Sum(nil)) != strings.TrimPrefix(release.Asset.Digest, "sha256:") {
		return errors.New("组件下载大小或 SHA-256 校验失败，未安装")
	}
	if err = ctx.Err(); err != nil {
		return errors.New("组件下载已取消，未安装")
	}
	m.setProgress(c, "verifying", n, n, nil)
	files, err := m.extract(c, packagePath, relative+"/payload")
	if err != nil {
		return err
	}
	prepared := componentDownload{ID: id, Release: release, Files: files}
	if err = componentWriteJSON(filepath.Join(dir, "manifest.json"), prepared); err != nil {
		return err
	}
	m.mu.Lock()
	m.prepared[c] = prepared
	m.mu.Unlock()
	m.setProgress(c, "ready", n, n, nil)
	return nil
}

// CURRENT is not rewritten. A controlled publication must retain exact package
// identity; per-component updates are permitted only in an unmanaged installation.
func (m *componentUpdateManager) installationPolicy() error {
	p, err := m.path("CURRENT.json")
	if err != nil {
		return err
	}
	var current struct {
		SchemaVersion int             `json:"schemaVersion"`
		Release       json.RawMessage `json:"release"`
		Binary        componentFile   `json:"binary"`
	}
	if err = componentReadJSON(p, &current); os.IsNotExist(err) {
		// A copied release manifest without CURRENT is still a controlled context.
		release, _ := m.path("release/current.json")
		if _, e := os.Lstat(release); !os.IsNotExist(e) {
			return errComponentControlled
		}
		return nil
	} else if err != nil {
		return errComponentControlled
	}
	if current.SchemaVersion != 1 || current.Binary.Path != "MihomoLite.exe" || !componentSHA256RE.MatchString(current.Binary.SHA256) || current.Binary.Size <= 0 {
		return errComponentControlled
	}
	if len(current.Release) > 0 && string(current.Release) != "null" {
		return errComponentControlled
	}
	// A root CURRENT identity is treated as a controlled installation even if
	// its release reference was omitted; absence never relaxes manifest policy.
	return errComponentControlled
}
func (m *componentUpdateManager) identity(c string) componentIdentity {
	result := componentIdentity{IdentityStatus: "unknown", ManifestStatus: "unmanaged-local"}
	target := componentTarget(c)
	if c == "launcher" {
		target = "MihomoLite.exe"
	}
	p, err := m.path(target)
	if err != nil {
		result.IdentityStatus = "unsafe-path"
		return result
	}
	var files []componentFile
	if c == "launcher" {
		h, size, e := componentHash(p)
		err = e
		if err == nil {
			files = []componentFile{{Path: "MihomoLite.exe", SHA256: h, Size: size}}
			result.SHA256 = h
			result.Size = size
		}
	} else {
		files, _, err = m.snapshot(c, p)
		if err == nil {
			result.SHA256, result.Size = componentTreeIdentity(files)
		}
	}
	if os.IsNotExist(err) || len(files) == 0 {
		result.IdentityStatus = "missing"
	} else if err != nil {
		result.IdentityStatus = "unreadable"
	} else {
		result.IdentityStatus = "local-hash"
	}
	m.mu.Lock()
	tx, ok := m.latest[c]
	m.mu.Unlock()
	if ok && componentFilesEqual(files, tx.After) {
		result.Version = tx.InstalledVersion
		result.IdentityStatus = "verified-update-manifest"
		result.ManifestStatus = "component-update-match"
	}
	cp, _ := m.path("CURRENT.json")
	var current struct {
		SchemaVersion int           `json:"schemaVersion"`
		Version       string        `json:"version"`
		Binary        componentFile `json:"binary"`
	}
	if e := componentReadJSON(cp, &current); e == nil {
		if c == "launcher" && current.SchemaVersion == 1 && current.Binary.Path == "MihomoLite.exe" && current.Binary.SHA256 == result.SHA256 && current.Binary.Size == result.Size && result.IdentityStatus != "missing" {
			result.Version = current.Version
			result.IdentityStatus = "local-current-match"
			result.ManifestStatus = "current-match"
		} else if c == "launcher" {
			result.ManifestStatus = "current-mismatch"
		}
	} else if !os.IsNotExist(e) {
		result.ManifestStatus = "current-invalid"
	}
	rp, e := m.path("release/current.json")
	if e != nil {
		result.ManifestStatus = "release-invalid"
		return result
	}
	var release struct {
		SchemaVersion int             `json:"schemaVersion"`
		Version       string          `json:"version"`
		Binary        componentFile   `json:"binary"`
		Files         []componentFile `json:"files"`
	}
	if e = componentReadJSON(rp, &release); e == nil {
		expected := []componentFile{}
		prefix := "MihomoLite/" + target
		for _, f := range release.Files {
			if (c == "panel" && strings.HasPrefix(f.Path, prefix+"/")) || (c != "panel" && f.Path == prefix) {
				name := filepath.Base(target)
				if c == "panel" {
					name = strings.TrimPrefix(f.Path, prefix+"/")
				}
				expected = append(expected, componentFile{Path: name, SHA256: f.SHA256, Size: f.Size})
			}
		}
		sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
		if release.SchemaVersion == 1 && len(expected) > 0 && componentFilesEqual(files, expected) {
			result.ManifestStatus = "controlled-release-match"
			if c == "launcher" && release.Binary.SHA256 == result.SHA256 {
				result.Version = release.Version
			}
		} else {
			result.ManifestStatus = "controlled-release-mismatch"
		}
	} else if !os.IsNotExist(e) {
		result.ManifestStatus = "release-invalid"
	}
	return result
}
func (m *componentUpdateManager) status(disconnected bool) any {
	policy := m.installationPolicy()
	blocked := m.isBlocked()
	result := []componentView{}
	for _, c := range []string{"launcher", "core", "panel"} {
		identity := m.identity(c)
		m.mu.Lock()
		v := m.views[c]
		prepared, ready := m.prepared[c]
		tx, rollback := m.latest[c]
		m.mu.Unlock()
		v.Current = identity
		v.RequiresDisconnect = c != "launcher" && !disconnected
		v.CanInstall = c != "launcher" && ready && policy == nil && !blocked && disconnected
		v.CanRollback = c != "launcher" && rollback && policy == nil && !blocked && disconnected
		v.RollbackID = tx.ID
		if c == "launcher" {
			v.InstallBlockReason = errComponentUnconfigured.Error()
		} else if blocked {
			v.InstallBlockReason = errComponentRecovery.Error()
		} else if policy != nil {
			v.InstallBlockReason = policy.Error()
		} else if !disconnected {
			v.InstallBlockReason = errComponentStopped.Error()
		} else if !ready {
			v.InstallBlockReason = "请先显式检查版本并完成可信下载校验"
		}
		_ = prepared
		result = append(result, v)
	}
	return struct {
		Components      []componentView `json:"components"`
		RecoveryBlocked bool            `json:"recoveryBlocked"`
	}{result, blocked}
}
