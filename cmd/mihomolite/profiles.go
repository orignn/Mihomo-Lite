package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type Profile struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Source          string                     `json:"source"` // local or subscription
	URL             string                     `json:"url,omitempty"`
	Traffic         *subscriptionTraffic       `json:"traffic,omitempty"`
	TrafficScanned  bool                       `json:"trafficScanned,omitempty"`
	UpdatePolicy    *subscriptionUpdatePolicy  `json:"updatePolicy,omitempty"`
	UpdateRetry     *subscriptionUpdateRetry   `json:"updateRetry,omitempty"`
	DownloadCache   *subscriptionDownloadCache `json:"downloadCache,omitempty"`
	CreatedAt       string                     `json:"createdAt"`
	UpdatedAt       string                     `json:"updatedAt"`
	Size            int64                      `json:"size"`
	Active          bool                       `json:"active"`
	Pending         bool                       `json:"pending"`
	Nodes           int                        `json:"nodes"`
	LastUpdate      *subscriptionUpdate        `json:"lastUpdate,omitempty"`
	AutoUpdateHours int                        `json:"autoUpdateHours,omitempty"`
	NextUpdateAt    string                     `json:"nextUpdateAt,omitempty"`
	HasUpdateDiff   bool                       `json:"hasUpdateDiff,omitempty"`
	HasBackup       bool                       `json:"hasBackup"`
}

type profileStore struct {
	ActiveID string    `json:"activeId"`
	Profiles []Profile `json:"profiles"`
}

type ProfileResponse struct {
	Profiles []Profile `json:"profiles"`
	ActiveID string    `json:"activeId"`
	Running  bool      `json:"running"`
	Pending  bool      `json:"pending"`
}

func (a *Application) profileDir() string       { return filepath.Join(a.dataDir, "profiles") }
func (a *Application) profileStorePath() string { return filepath.Join(a.dataDir, "profiles.json") }
func (a *Application) profileContentPath(id string) string {
	return filepath.Join(a.profileDir(), id+".yaml")
}
func (a *Application) pendingProfilePath() string { return filepath.Join(a.dataDir, "pending-profile") }

func validProfileID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func normalizeProfileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("请填写配置名称")
	}
	if utf8.RuneCountInString(name) > 80 || strings.IndexByte(name, 0) >= 0 || strings.ContainsAny(name, "\r\n") {
		return "", errors.New("配置名称无效或超过 80 个字符")
	}
	return name, nil
}

func normalizeSubscriptionURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("订阅地址无效或超过 8192 字节")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("订阅地址必须是有效的 http:// 或 https:// URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("订阅地址必须是有效的 http:// 或 https:// URL，且不能包含用户信息")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number <= 0 || number > 65535 {
			return "", errors.New("订阅地址端口无效")
		}
	}
	parsed.Fragment, parsed.RawFragment = "", ""
	return parsed.String(), nil
}

func (a *Application) loadProfileStore() (profileStore, error) {
	var store profileStore
	content, err := os.ReadFile(a.profileStorePath())
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return store, err
	}
	if err := json.Unmarshal(content, &store); err != nil {
		return store, errors.New("profiles.json 格式无效")
	}
	if len(store.Profiles) > 100 {
		return store, errors.New("配置数量超过限制")
	}
	ids := make(map[string]bool)
	for i := range store.Profiles {
		p := &store.Profiles[i]
		if !validProfileID(p.ID) || ids[p.ID] {
			return store, errors.New("配置列表包含无效或重复 ID")
		}
		ids[p.ID] = true
		if p.Source != "local" && p.Source != "subscription" {
			return store, errors.New("配置类型无效")
		}
		if _, err := normalizeProfileName(p.Name); err != nil {
			return store, err
		}
		if p.Source == "subscription" {
			if _, err := normalizeSubscriptionURL(p.URL); err != nil {
				return store, err
			}
		}
		if !fileExists(a.profileContentPath(p.ID)) {
			return store, fmt.Errorf("配置“%s”的内容文件丢失", p.Name)
		}
		if p.Source != "subscription" || !validSubscriptionTraffic(p.Traffic) {
			p.Traffic = nil
		}
		sanitizeSubscriptionMetadata(p, time.Now())
		p.Pending = false
		p.HasUpdateDiff = false
		p.Active = p.ID == store.ActiveID
	}
	if store.ActiveID != "" && !ids[store.ActiveID] {
		return store, errors.New("当前配置不存在")
	}
	return store, nil
}

func encodeProfileStore(store profileStore) ([]byte, error) {
	for i := range store.Profiles {
		p := &store.Profiles[i]
		if !validSubscriptionProfileMetadata(*p) {
			return nil, errors.New("订阅更新元数据无效")
		}
		store.Profiles[i].Active = store.Profiles[i].ID == store.ActiveID
		store.Profiles[i].Pending = false
		store.Profiles[i].HasUpdateDiff = false
	}
	content, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

func (a *Application) saveProfileStore(store profileStore) error {
	content, err := encodeProfileStore(store)
	if err != nil {
		return err
	}
	if err := atomicWrite(a.profileStorePath(), content, 0o600); err != nil {
		return err
	}
	a.profileRevision.Add(1)
	return nil
}

func (a *Application) sourceYAMLPath() string {
	// The pending marker signals a reconnect, not another profile selection. A
	// stale marker must never choose source bytes from a different stable ID.
	if store, err := a.loadProfileStore(); err == nil && store.ActiveID != "" {
		return a.profileContentPath(store.ActiveID)
	}
	return filepath.Join(a.dataDir, "source.yaml")
}

func (a *Application) syncCurrentSource() error {
	path := a.sourceYAMLPath()
	if path == filepath.Join(a.dataDir, "source.yaml") {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.dataDir, "source.yaml"), content, 0o600)
}

func (a *Application) Profiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		response, err := a.profileResponse()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPost:
		if err := a.begin(); err != nil {
			writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
			return
		}
		var req struct {
			Action  string                    `json:"action"`
			ID      string                    `json:"id"`
			Name    string                    `json:"name"`
			URL     string                    `json:"url"`
			Mode    string                    `json:"mode"`
			Content string                    `json:"content"`
			Hours   *int                      `json:"hours"`
			Policy  *subscriptionUpdatePolicy `json:"policy"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
		err := decodeJSON(r, &req)
		if err == nil {
			if req.Action == "auto-update" {
				err = a.setSubscriptionAutoUpdate(req.ID, req.Hours, time.Now())
			} else {
				err = a.handleProfileActionWithPolicy(a.subscriptionContext(r.Context()), req.Action, req.ID, req.Name, req.URL, req.Mode, req.Content, req.Policy)
			}
		}
		a.finish(err)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		response, responseErr := a.profileResponse()
		if responseErr != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: responseErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, response)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "不支持的请求方法"})
	}
}

func (a *Application) profileResponse() (ProfileResponse, error) {
	store, err := a.loadProfileStore()
	if err != nil {
		return ProfileResponse{}, err
	}
	pendingID := ""
	if content, err := os.ReadFile(a.pendingProfilePath()); err == nil {
		pendingID = strings.TrimSpace(string(content))
	}
	a.mu.Lock()
	running := a.core != nil
	a.mu.Unlock()
	for i := range store.Profiles {
		p := &store.Profiles[i]
		p.DownloadCache = nil // Private validators never leave the profile API.
		p.Pending = running && p.ID == pendingID
		p.HasUpdateDiff = p.Source == "subscription" && fileExists(a.subscriptionDiffPath(p.ID))
		p.HasBackup = regularFileExists(a.profileContentPath(p.ID) + ".bak")
		if content, err := os.ReadFile(a.profileContentPath(p.ID)); err == nil {
			var parsed struct {
				Proxies        []yaml.Node          `yaml:"proxies"`
				ProxyProviders map[string]yaml.Node `yaml:"proxy-providers"`
			}
			if yaml.Unmarshal(content, &parsed) == nil {
				p.Nodes = len(parsed.Proxies) + len(parsed.ProxyProviders)
			}
			// An older subscription may have saved the node hints before this
			// parser existed. Show its snapshot without writing metadata or
			// overriding unknown fields from a response we've already scanned.
			if p.Source == "subscription" && !p.TrafficScanned && p.Traffic == nil {
				if at, err := time.Parse(time.RFC3339Nano, p.UpdatedAt); err == nil {
					p.Traffic = parseSubscriptionNodeInfo(content, at.UTC())
				}
			}
		}
	}
	sort.SliceStable(store.Profiles, func(i, j int) bool { return store.Profiles[i].CreatedAt < store.Profiles[j].CreatedAt })
	return ProfileResponse{Profiles: store.Profiles, ActiveID: store.ActiveID, Running: running, Pending: pendingID != ""}, nil
}

func (a *Application) handleProfileAction(ctx context.Context, action, id, name, rawURL, mode, content string) error {
	return a.handleProfileActionWithPolicy(ctx, action, id, name, rawURL, mode, content, nil)
}

func (a *Application) handleProfileActionWithPolicy(ctx context.Context, action, id, name, rawURL, mode, content string, policy *subscriptionUpdatePolicy) error {
	if policy != nil && action != "add-url" && action != "edit-url" && action != "update-policy" {
		return errors.New("此操作不接受候选订阅策略，请使用保存策略操作")
	}
	switch action {
	case "add-url":
		return a.addSubscriptionWithPolicy(ctx, name, rawURL, mode, policy)
	case "add-local":
		return a.addLocalProfile(name, content)
	case "update":
		return a.updateSubscription(ctx, id, mode)
	case "edit-url":
		return a.updateSubscriptionURLWithPolicy(ctx, id, rawURL, mode, policy)
	case "update-policy":
		return a.setSubscriptionUpdatePolicy(id, policy)
	case "switch":
		if err := a.switchProfile(id); err != nil {
			return err
		}
		return a.reconnectAfterProfileChange(id)
	case "rename":
		return a.renameProfile(id, name)
	case "edit":
		return a.editActiveProfile(id, content)
	case "rollback":
		return a.rollbackProfile(id)
	case "delete":
		return a.deleteProfile(id)
	default:
		return errors.New("未知配置操作")
	}
}

func newProfileID(content []byte) string {
	sum := sha256.Sum256(append(content, []byte(time.Now().Format(time.RFC3339Nano))...))
	return hex.EncodeToString(sum[:])
}

func (a *Application) validateProfileContent(content []byte) ([]byte, error) {
	return a.validateProfileContentContext(context.Background(), content)
}

func (a *Application) validateProfileContentContext(ctx context.Context, content []byte) ([]byte, error) {
	return a.validateProfileContentForID(ctx, "", content)
}

func (a *Application) validateProfileContentForID(ctx context.Context, id string, content []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(content) == 0 || len(content) > 4<<20 {
		return nil, errors.New("配置为空或超过 4 MiB")
	}
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		if scheme, found := detectShareLinkSubscription(content); found {
			return nil, fmt.Errorf("这是 %s 分享链接订阅，当前版本暂不支持；请使用返回完整 Clash/mihomo YAML 的订阅地址，或改用“粘贴配置”导入转换后的 YAML", scheme)
		}
		return nil, errors.New("订阅内容不是 Clash/mihomo YAML 配置")
	}
	effective, err := materializeConfigRoot(root.Content[0])
	if err != nil {
		return nil, err
	}
	if mappingValue(effective, "proxies") == nil && mappingValue(effective, "proxy-providers") == nil {
		return nil, errors.New("YAML 中没有 proxies 或 proxy-providers，可能不是完整配置")
	}
	if a.logger != nil {
		a.logger.Info("正在校验新配置")
	}
	_, rendered, err := a.prepareProfileConfig(content, id, RuntimeConfig{}, nil, nil)
	if err != nil {
		return nil, err
	}
	if err := a.testConfigContext(ctx, rendered); err != nil {
		return nil, err
	}
	return content, nil
}

func (a *Application) addLocalProfile(name, content string) error {
	name, err := normalizeProfileName(name)
	if err != nil {
		return err
	}
	data, err := a.normalizeProfileInput([]byte(content), a.activeProfileTemplate())
	if err != nil {
		return err
	}
	data, err = a.validateProfileContent(data)
	if err != nil {
		return err
	}
	id := newProfileID(data)
	if err := atomicWrite(a.profileContentPath(id), data, 0o600); err != nil {
		return err
	}
	store, err := a.loadProfileStore()
	if err != nil {
		return err
	}
	if len(store.Profiles) >= 100 {
		return errors.New("最多保存 100 个配置")
	}
	now := time.Now().Format(time.RFC3339)
	store.Profiles = append(store.Profiles, Profile{ID: id, Name: name, Source: "local", CreatedAt: now, UpdatedAt: now, Size: int64(len(data))})
	store.ActiveID = id
	if err := a.saveProfileStore(store); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "source.yaml"), data, 0o600); err != nil {
		return err
	}
	if err := a.markProfilePending(id); err != nil {
		return err
	}
	if err := a.setConfigName(name); err != nil {
		return err
	}
	if a.logger != nil {
		a.logger.Info("本地配置已添加并选中", "name", name)
	}
	return nil
}

func (a *Application) addSubscription(ctx context.Context, name, rawURL, mode string) error {
	return a.addSubscriptionWithPolicy(ctx, name, rawURL, mode, nil)
}

func (a *Application) addSubscriptionWithPolicy(ctx context.Context, name, rawURL, mode string, policy *subscriptionUpdatePolicy) error {
	name, err := normalizeProfileName(name)
	if err != nil {
		return err
	}
	address, err := normalizeSubscriptionURL(rawURL)
	if err != nil {
		return err
	}
	policy, err = normalizeSubscriptionUpdatePolicy(policy)
	if err != nil {
		return err
	}
	store, err := a.loadProfileStore()
	if err != nil {
		return errors.New("无法读取配置列表，未添加订阅")
	}
	if len(store.Profiles) >= 100 {
		return errors.New("最多保存 100 个配置")
	}
	download, err := a.downloadSubscriptionWithPolicy(ctx, address, mode, policy, nil, nil)
	if err != nil {
		return err
	}
	data, err := a.normalizeProfileInput(download.Content, a.activeProfileTemplate())
	if err != nil {
		if safe := safeShareLinkConversionError(err); safe != nil {
			return safe
		}
		return errors.New("订阅内容解析失败，未添加配置；请使用受支持的完整 Clash/mihomo YAML")
	}
	data, err = a.validateProfileContentContext(ctx, data)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("订阅内容校验失败，未添加配置: %w", safeSubscriptionValidationError(err))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id := newProfileID(data)
	now := time.Now().Format(time.RFC3339)
	store.Profiles = append(store.Profiles, Profile{ID: id, Name: name, Source: "subscription", URL: address, Traffic: download.Traffic, TrafficScanned: true, UpdatePolicy: policy, DownloadCache: download.cacheForContent(data), CreatedAt: now, UpdatedAt: now, Size: int64(len(data))})
	store.ActiveID = id
	encoded, err := encodeProfileStore(store)
	if err != nil {
		return errors.New("无法准备订阅信息，未添加配置")
	}
	settings, err := a.settingsWithProfileName(name)
	if err != nil {
		return errors.New("无法准备订阅设置，未添加配置")
	}
	a.mu.Lock()
	running := a.core != nil
	a.mu.Unlock()
	changes := []backupFileChange{{Path: "profiles/" + id + ".yaml", Content: data}, {Path: "source.yaml", Content: data}, {Path: "@launcher.json", Content: settings}, {Path: "profiles.json", Content: encoded}}
	if running {
		changes = append(changes, backupFileChange{Path: "pending-profile", Content: []byte(id)})
	} else {
		changes = append(changes, backupFileChange{Path: "pending-profile", Remove: true})
	}
	// Reuse the existing prepare/commit/rollback journal, including selection
	// and config name. Candidate URL, policy and validators become visible only
	// when every content/metadata write has committed successfully.
	if checkBackupPath(a.dataDir) != nil {
		return errors.New("配置目录路径无效，未添加订阅")
	}
	if err := os.MkdirAll(a.dataDir, 0o700); err != nil {
		return errors.New("无法准备配置目录，未添加订阅")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.commitBackupFiles(changes); err != nil {
		return errors.New("添加订阅提交未完成；已尝试回退原配置，未完成事务会保留恢复快照")
	}
	a.mu.Lock()
	if a.config != nil {
		a.config.ConfigName = name
	}
	a.mu.Unlock()
	a.profileRevision.Add(1)
	if a.logger != nil {
		a.logger.Info("订阅已添加并选中", "bytes", len(data))
	}
	return nil
}

func (a *Application) switchProfile(id string) error {
	store, err := a.loadProfileStore()
	if err != nil {
		return err
	}
	index := profileIndex(store, id)
	if index < 0 {
		return errors.New("配置不存在")
	}
	content, err := os.ReadFile(a.profileContentPath(id))
	if err != nil {
		return err
	}
	// Reject incompatible global rules before changing selection or stopping the
	// existing connection. This static preflight does not start a core or write.
	if _, _, err := a.prepareProfileConfig(content, id, RuntimeConfig{}, nil, nil); err != nil {
		return fmt.Errorf("配置未切换，原配置保持不变: %w", err)
	}
	store.ActiveID = id
	if err := a.saveProfileStore(store); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "source.yaml"), content, 0o600); err != nil {
		return err
	}
	if err := a.markProfilePending(id); err != nil {
		return err
	}
	if err := a.setConfigName(store.Profiles[index].Name); err != nil {
		return err
	}
	if a.logger != nil {
		a.logger.Info("已切换配置", "name", store.Profiles[index].Name)
	}
	return nil
}

func (a *Application) editActiveProfile(id, content string) error {
	store, err := a.loadProfileStore()
	if err != nil {
		return err
	}
	if store.ActiveID == "" {
		return errors.New("当前没有可编辑的配置")
	}
	index := profileIndex(store, store.ActiveID)
	if index < 0 {
		return errors.New("当前配置不存在")
	}
	if id != "" && id != store.ActiveID {
		return errors.New("当前配置已切换，请重新打开 YAML 编辑器")
	}
	path := a.profileContentPath(store.ActiveID)
	old, err := os.ReadFile(path)
	if err != nil {
		return errors.New("无法读取当前配置，未执行保存")
	}
	if bytes.Equal(bytes.ReplaceAll(old, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll([]byte(content), []byte("\r\n"), []byte("\n"))) {
		return nil // Preserve the actual previous version for a no-op save.
	}
	data, err := a.validateProfileContentForID(context.Background(), store.ActiveID, []byte(content))
	if err != nil {
		return err
	}
	if err := atomicWrite(path+".bak", old, 0o600); err != nil {
		return err
	}
	if err := atomicWrite(path, data, 0o600); err != nil {
		return err
	}
	profile := store.Profiles[index]
	profile.UpdatedAt = time.Now().Format(time.RFC3339)
	profile.Size = int64(len(data))
	profile.DownloadCache = nil // Local YAML changes invalidate committed validators.
	store.Profiles[index] = profile
	if err := a.saveProfileStore(store); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "source.yaml"), data, 0o600); err != nil {
		return err
	}
	if err := a.markProfilePending(store.ActiveID); err != nil {
		return err
	}
	return a.setConfigName(profile.Name)
}

func (a *Application) renameProfile(id, name string) error {
	name, err := normalizeProfileName(name)
	if err != nil {
		return err
	}
	store, err := a.loadProfileStore()
	if err != nil {
		return err
	}
	index := profileIndex(store, id)
	if index < 0 {
		return errors.New("配置不存在")
	}
	store.Profiles[index].Name = name
	if err := a.saveProfileStore(store); err != nil {
		return err
	}
	if store.ActiveID == id {
		if err := a.setConfigName(name); err != nil {
			return err
		}
	}
	return nil
}

func (a *Application) deleteProfile(id string) error {
	store, err := a.loadProfileStore()
	if err != nil {
		return err
	}
	index := profileIndex(store, id)
	if index < 0 {
		return errors.New("配置不存在")
	}
	if store.ActiveID == id {
		return errors.New("不能删除当前配置，请先切换到其他配置")
	}
	store.Profiles = append(store.Profiles[:index], store.Profiles[index+1:]...)
	if err := a.saveProfileStore(store); err != nil {
		return err
	}
	// Keep content and backup after metadata removal for an inexpensive recovery path.
	return os.Rename(a.profileContentPath(id), a.profileContentPath(id)+".deleted")
}

func (a *Application) markProfilePending(id string) error {
	a.mu.Lock()
	running := a.core != nil
	a.mu.Unlock()
	if running {
		return atomicWrite(a.pendingProfilePath(), []byte(id), 0o600)
	}
	_ = os.Remove(a.pendingProfilePath())
	return nil
}

func profileIndex(store profileStore, id string) int {
	if !validProfileID(id) {
		return -1
	}
	for i := range store.Profiles {
		if store.Profiles[i].ID == id {
			return i
		}
	}
	return -1
}

func (a *Application) downloadSubscriptionWithInfo(ctx context.Context, address, mode string) (subscriptionDownload, error) {
	download, err := a.downloadSubscriptionWithPolicy(ctx, address, mode, nil, nil, nil)
	return download.subscriptionDownload, err
}

func (a *Application) downloadSubscription(ctx context.Context, address, mode string) ([]byte, error) {
	download, err := a.downloadSubscriptionWithInfo(ctx, address, mode)
	if err != nil {
		return nil, err
	}
	return download.Content, nil
}

func (a *Application) profileCount() int {
	store, err := a.loadProfileStore()
	if err != nil {
		return 0
	}
	return len(store.Profiles)
}

// detectShareLinkSubscription recognizes a link collection at its start, not
// URLs embedded in YAML metadata. Base64 may be short, URL-safe or line-wrapped.
func detectShareLinkSubscription(content []byte) (string, bool) {
	text, err := shareText(content)
	if err != nil {
		return "", false
	}
	scheme := shareScheme(text)
	return scheme, scheme != ""
}

// The profile request owns a.operation through both saving and reconnecting.
func (a *Application) reconnectAfterProfileChange(id string) error {
	store, err := a.loadProfileStore()
	if err != nil {
		return err
	}
	if store.ActiveID != id {
		return nil
	}
	a.mu.Lock()
	running, preserve := a.core != nil && !a.shuttingDown, a.runtime
	a.mu.Unlock()
	if !running {
		return nil
	}
	if err := a.restartCore(preserve); err != nil {
		return fmt.Errorf("配置已保存，但自动重连失败: %w", err)
	}
	return nil
}

func (a *Application) restartCore(preserve RuntimeConfig) error {
	return a.restartCoreForPolicy(preserve)
}

func (a *Application) requestReconnect() error {
	return a.requestReconnectPolicy()
}

func (a *Application) Reconnect() {
	if err := a.requestReconnect(); err != nil {
		a.logger.Error("自动重连失败", "error", err)
	}
}

// normalizeProfileInput converts share-link subscriptions into YAML before
// validation; plain YAML passes through unchanged. A rule template (an
// existing complete profile) is merged in so share-link nodes inherit the
// user's routing rules, DNS and rule-providers.
func (a *Application) normalizeProfileInput(content, template []byte) ([]byte, error) {
	// A valid full configuration may contain share links in comments/metadata.
	// Keep it intact before attempting link conversion.
	var candidate yaml.Node
	if yaml.Unmarshal(content, &candidate) == nil && len(candidate.Content) == 1 && candidate.Content[0].Kind == yaml.MappingNode {
		if effective, err := materializeConfigRoot(candidate.Content[0]); err == nil && (mappingValue(effective, "proxies") != nil || mappingValue(effective, "proxy-providers") != nil) {
			return content, nil
		}
	}
	if scheme, found := detectShareLinkSubscription(content); found {
		base, err := convertShareLinkSubscription(content, scheme)
		if err != nil {
			return nil, err
		}
		if len(template) > 0 {
			return mergeShareLinkWithTemplate(base, template)
		}
		return base, nil
	}
	return content, nil
}

// activeProfileTemplate returns the current active profile content when it is
// a useful rule template (it carries more than the trivial MATCH rule).
func (a *Application) activeProfileTemplate() []byte {
	store, err := a.loadProfileStore()
	if err != nil || store.ActiveID == "" {
		return nil
	}
	content, err := os.ReadFile(a.profileContentPath(store.ActiveID))
	if err != nil {
		return nil
	}
	if hasRichRules(content) {
		return content
	}
	return nil
}

func hasRichRules(content []byte) bool {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil || len(root.Content) != 1 {
		return false
	}
	rootNode, err := materializeConfigRoot(root.Content[0])
	if err != nil {
		return false
	}
	if mappingValue(rootNode, "rule-providers") != nil {
		return true
	}
	if node := mappingValue(rootNode, "rules"); node != nil && len(node.Content) > 2 {
		return true
	}
	return false
}
