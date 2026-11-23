package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const maxOverrideYAMLBytes = 256 << 10
const maxOverrideStoreBytes = 8 << 20
const overrideSecretPlaceholder = "<redacted-api-secret>"
const overrideWarning = "最终配置包含节点密码、订阅地址等秘密，请勿公开分享；启动器 API secret 已脱敏。预览只读，不写运行配置、不连接节点、不下载资源。"

// Stored independently of subscriptions; keys are immutable profile IDs, never names or positions.
type overrideStore struct {
	SchemaVersion int               `json:"schemaVersion"`
	Global        string            `json:"global"`
	Profiles      map[string]string `json:"profiles"`
}

type overrideRequest struct {
	Scope     string  `json:"scope"`
	ProfileID string  `json:"profileId"`
	YAML      *string `json:"yaml"`
}

type configPreviewResponse struct {
	ProfileID  string   `json:"profileId"`
	YAML       string   `json:"yaml"`
	Precedence []string `json:"precedence"`
	Validated  bool     `json:"validated"`
	Validation string   `json:"validation"`
	Warning    string   `json:"warning"`
}

func overridePrecedence() []string {
	return []string{"原始订阅/配置", "全局覆写", "此配置覆写（稳定 profile ID）", "统一代理运行层", "自定义规则（前置）", "Lite 本机监听/端口/API secret/TUN 安全限制（最终强制）"}
}

func emptyOverrideStore() overrideStore {
	return overrideStore{SchemaVersion: 1, Profiles: map[string]string{}}
}
func (a *Application) overridesPath() string { return filepath.Join(a.dataDir, "overrides.json") }

func checkOverrideFilePath(path string) error {
	if err := checkBackupPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular override/resource file")
	}
	return nil
}

// A single mapping document is required; extra documents and duplicate/complex keys are not ignored.
func decodeOverrideYAML(text string) (*yaml.Node, error) {
	if len(text) > maxOverrideYAMLBytes || !utf8.ValidString(text) {
		return nil, errors.New("覆写必须为 UTF-8，且不能超过 256 KiB")
	}
	if strings.TrimSpace(text) == "" {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	dec := yaml.NewDecoder(strings.NewReader(text))
	var doc, extra yaml.Node
	if dec.Decode(&doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("覆写必须是单份 YAML 映射")
	}
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("覆写不支持多个 YAML 文档")
	}
	var checkKeys func(*yaml.Node) error
	checkKeys = func(n *yaml.Node) error {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind != yaml.ScalarNode || (key.Tag != "!!str" && key.Tag != "!!merge") {
					return errors.New("覆写映射键必须是字符串")
				}
			}
		}
		for _, child := range n.Content {
			if err := checkKeys(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := checkKeys(doc.Content[0]); err != nil {
		return nil, err
	}
	root, err := materializeConfigRoot(doc.Content[0])
	if err != nil {
		return nil, errors.New("覆写包含重复键、无效字段类型或循环引用")
	}
	var check func(*yaml.Node, int) error
	check = func(n *yaml.Node, depth int) error {
		if depth > 64 {
			return errors.New("覆写嵌套超过 64 层")
		}
		if n.Tag == "!!null" {
			return errors.New("覆写不接受 null；请删除该键以继承下层，或使用空数组替换数组")
		}
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Kind != yaml.ScalarNode || n.Content[i].Tag != "!!str" {
					return errors.New("覆写映射键必须是字符串")
				}
			}
		}
		for _, child := range n.Content {
			if err := check(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(root, 0); err != nil {
		return nil, err
	}
	if mappingValue(root, unifiedProxyMarker) != nil {
		return nil, errors.New("覆写不能设置启动器统一代理所有权标记")
	}
	return root, nil
}

// Exposed to backup integration: nil/legacy means an empty independent layer.
func normalizeOverrideStore(store *overrideStore) (overrideStore, error) {
	result := emptyOverrideStore()
	if store == nil {
		return result, nil
	}
	if store.SchemaVersion != 1 || len(store.Profiles) > 100 {
		return result, errors.New("覆写存储版本或配置数量无效")
	}
	if _, err := decodeOverrideYAML(store.Global); err != nil {
		return result, err
	}
	result.Global = store.Global
	for id, text := range store.Profiles {
		if !validProfileID(id) || id != strings.ToLower(id) {
			return result, errors.New("覆写配置 ID 无效")
		}
		if _, err := decodeOverrideYAML(text); err != nil {
			return result, err
		}
		result.Profiles[id] = text
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > maxOverrideStoreBytes {
		return result, errors.New("覆写存储超过 8 MiB")
	}
	return result, nil
}
func (a *Application) loadOverrides() (overrideStore, error) {
	if a.dataDir == "" {
		return emptyOverrideStore(), nil
	}
	if err := checkOverrideFilePath(a.overridesPath()); err != nil {
		return emptyOverrideStore(), errors.New("覆写路径不安全")
	}
	f, err := os.Open(a.overridesPath())
	if errors.Is(err, os.ErrNotExist) {
		return emptyOverrideStore(), nil
	}
	if err != nil {
		return emptyOverrideStore(), errors.New("无法读取覆写")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxOverrideStoreBytes+1))
	if err != nil || len(data) > maxOverrideStoreBytes {
		return emptyOverrideStore(), errors.New("覆写文件无法读取或超过限制")
	}
	if rejectDuplicateJSONKeys(data) != nil || requireJSONFields(data, []string{"schemaVersion", "global", "profiles"}, nil) != nil {
		return emptyOverrideStore(), errors.New("overrides.json 包含重复键、缺失或未知字段")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if len(fields["global"]) == 0 || fields["global"][0] != '"' || len(fields["profiles"]) == 0 || fields["profiles"][0] != '{' {
		return emptyOverrideStore(), errors.New("overrides.json 字段类型无效")
	}
	var store overrideStore
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&store) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return emptyOverrideStore(), errors.New("overrides.json 格式无效")
	}
	return normalizeOverrideStore(&store)
}

// Mapping+mapping recurses; sequences/scalars replace. Empty mapping is inheritance, [] clears an array.
func mergeOverrideMapping(dst, src *yaml.Node) {
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, value := src.Content[i].Value, src.Content[i+1]
		old := mappingValue(dst, key)
		if old != nil && old.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			mergeOverrideMapping(old, value)
		} else {
			setMapping(dst, key, value)
		}
	}
}
func mergeConfigOverrides(content []byte, id string, store overrideStore) ([]byte, error) {
	if id != "" && !validProfileID(id) {
		return nil, errors.New("覆写配置 ID 无效")
	}
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(content))
	if dec.Decode(&doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("配置必须是单份 YAML 映射")
	}
	var extra yaml.Node
	if dec.Decode(&extra) != io.EOF {
		return nil, errors.New("配置不支持多个 YAML 文档")
	}
	root, err := materializeConfigRoot(doc.Content[0])
	if err != nil {
		return nil, err
	}
	for _, text := range []string{store.Global, store.Profiles[id]} {
		layer, err := decodeOverrideYAML(text)
		if err != nil {
			return nil, err
		}
		mergeOverrideMapping(root, layer)
	}
	return yaml.Marshal(root)
}
func (a *Application) prepareProfileConfig(content []byte, id string, runtime RuntimeConfig, custom []CustomRule, store *overrideStore) (RuntimeConfig, []byte, error) {
	var layers overrideStore
	var err error
	if store == nil {
		layers, err = a.loadOverrides()
	} else {
		layers, err = normalizeOverrideStore(store)
	}
	if err != nil {
		return runtime, nil, err
	}
	merged, err := mergeConfigOverrides(content, id, layers)
	if err != nil {
		return runtime, nil, err
	}
	return a.prepareConfigBase(merged, runtime, custom)
}

// DNS listen is an additional inbound independent of allow-lan, hence protected separately.
func enforceLoopbackDNS(root *yaml.Node) error {
	dns := mappingValue(root, "dns")
	if dns == nil {
		return nil
	}
	if dns.Kind != yaml.MappingNode {
		return errors.New("dns 必须为 YAML 映射")
	}
	listen := mappingValue(dns, "listen")
	if listen == nil {
		return nil
	}
	if listen.Kind != yaml.ScalarNode || listen.Tag != "!!str" {
		return errors.New("DNS listen 必须为 host:port 字符串")
	}
	if strings.TrimSpace(listen.Value) == "" {
		setScalar(dns, "listen", "")
		return nil
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen.Value))
	number, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || number < 1 || number > 65535 {
		return errors.New("DNS listen 必须为有效的 host:port")
	}
	if host == "::1" {
		setScalar(dns, "listen", net.JoinHostPort("::1", port))
	} else {
		setScalar(dns, "listen", net.JoinHostPort("127.0.0.1", port))
	}
	return nil
}

// Deterministic display/validation endpoints: no port binding, writes, or secret generation.
// Auto ports here are placeholders; real connection still selects available ports.
func (a *Application) previewRuntime() RuntimeConfig {
	a.mu.Lock()
	current := a.runtime
	a.mu.Unlock()
	ports, web := a.runtimePortSnapshot()
	used := map[int]bool{web: true}
	next := 20000
	choose := func(existing int, option PortOption) int {
		if existing > 0 && !used[existing] {
			used[existing] = true
			return existing
		}
		if option.Mode == "fixed" && option.Port > 0 && !used[option.Port] {
			used[option.Port] = true
			return option.Port
		}
		for used[next] {
			next++
		}
		value := next
		used[value] = true
		next++
		return value
	}
	current.MixedPort = choose(current.MixedPort, ports.Proxy)
	current.BackendPort = choose(current.BackendPort, ports.Backend)
	current.APIPort = choose(current.APIPort, ports.Panel)
	current.Secret = overrideSecretPlaceholder
	return current
}
func overrideScope(scope, id string, profiles profileStore) error {
	if scope == "global" {
		if id != "" {
			return errors.New("全局覆写保存不接受 profileId")
		}
		return nil
	}
	if scope != "profile" {
		return errors.New("scope 必须为 global 或 profile")
	}
	if profileIndex(profiles, id) < 0 {
		return errors.New("配置不存在")
	}
	return nil
}
func withOverrideCandidate(store overrideStore, scope, id, text string) (overrideStore, error) {
	result, err := normalizeOverrideStore(&store)
	if err != nil {
		return result, err
	}
	layer, err := decodeOverrideYAML(text)
	if err != nil {
		return result, err
	}
	if len(layer.Content) == 0 {
		text = ""
	}
	if scope == "global" {
		result.Global = text
	} else {
		if text == "" {
			delete(result.Profiles, id)
		} else {
			result.Profiles[id] = text
		}
	}
	return normalizeOverrideStore(&result)
}

// Caller owns Application.operation. Commit only after every affected profile validates.
func (a *Application) saveOverrides(ctx context.Context, scope, id, text string) (int, error) {
	profiles, err := a.loadProfileStore()
	if err != nil {
		return 0, errors.New("无法读取配置列表，未保存覆写")
	}
	if err := overrideScope(scope, id, profiles); err != nil {
		return 0, err
	}
	old, err := a.loadOverrides()
	if err != nil {
		return 0, err
	}
	// A deleted profile is intentionally excluded from the next successful save,
	// not removed halfway through the existing non-transactional delete action.
	for savedID := range old.Profiles {
		if profileIndex(profiles, savedID) < 0 {
			delete(old.Profiles, savedID)
		}
	}
	candidate, err := withOverrideCandidate(old, scope, id, text)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, profile := range profiles.Profiles {
		if scope == "profile" && profile.ID != id {
			continue
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		content, err := os.ReadFile(a.profileContentPath(profile.ID))
		if err != nil {
			return 0, errors.New("无法读取受影响配置，未保存覆写")
		}
		_, rendered, err := a.prepareProfileConfig(content, profile.ID, a.previewRuntime(), nil, &candidate)
		if err == nil {
			err = a.testConfigOfflineContext(ctx, rendered)
		}
		if err != nil {
			return 0, fmt.Errorf("第 %d 个受影响配置未通过离线校验，原覆写保持不变：%w", count+1, safeOverrideValidationError(err))
		}
		count++
	}
	// No profiles yet: validate the global layer against the fixed empty-node template.
	if count == 0 {
		_, rendered, err := a.prepareProfileConfig([]byte(defaultConfig), "", a.previewRuntime(), []CustomRule{}, &candidate)
		if err == nil {
			err = a.testConfigOfflineContext(ctx, rendered)
		}
		if err != nil {
			return 0, safeOverrideValidationError(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	encoded, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := checkOverrideFilePath(a.overridesPath()); err != nil {
		return 0, errors.New("覆写路径不安全，未保存")
	}
	if err := atomicWrite(a.overridesPath(), append(encoded, '\n'), 0o600); err != nil {
		return 0, errors.New("覆写提交失败，原覆写保持不变")
	}
	return count, nil
}

type offlineValidationUnavailable struct{ reason string }

func (e *offlineValidationUnavailable) Error() string {
	return "未进行官方内核校验：" + e.reason + "；预览/覆写保存不会联网下载资源"
}

// Turn only the validation copy of cached HTTP providers into file providers. Missing
// caches fail closed. Neither source YAML nor the preview's effective YAML is rewritten.
func (a *Application) offlineValidationInput(content []byte) ([]byte, map[string][]byte, error) {
	missing := func(reason string) ([]byte, map[string][]byte, error) {
		return nil, nil, &offlineValidationUnavailable{reason: reason}
	}
	if !regularFileExists(filepath.Join(a.baseDir, "runtime", coreExecutable)) {
		return missing("尚未安装本地官方内核")
	}
	var doc yaml.Node
	if yaml.Unmarshal(content, &doc) != nil || len(doc.Content) != 1 {
		return missing("配置结构无效")
	}
	root, err := materializeConfigRoot(doc.Content[0])
	if err != nil {
		return nil, nil, err
	}
	resources := map[string][]byte{}
	readResource := func(path string) ([]byte, error) {
		if path == "" || filepath.IsAbs(path) || strings.ContainsAny(path, ":\x00") || strings.HasPrefix(path, "\\") {
			return nil, errors.New("resource path")
		}
		normalized := filepath.Clean(filepath.FromSlash(path))
		if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, ".."+string(filepath.Separator)) {
			return nil, errors.New("resource path")
		}
		if checkOverrideFilePath(filepath.Join(a.dataDir, normalized)) != nil {
			return nil, errors.New("resource path")
		}
		f, err := os.Open(filepath.Join(a.dataDir, normalized))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 32<<20 {
			return nil, errors.New("resource size")
		}
		return io.ReadAll(io.LimitReader(f, (32<<20)+1))
	}
	for _, kind := range []string{"proxy-providers", "rule-providers"} {
		providers := mappingValue(root, kind)
		if providers == nil {
			continue
		}
		if providers.Kind != yaml.MappingNode {
			return missing("provider 结构无效")
		}
		for i := 1; i < len(providers.Content); i += 2 {
			provider := providers.Content[i]
			if provider.Kind != yaml.MappingNode {
				return missing("provider 结构无效")
			}
			var parsed struct {
				Type        string `yaml:"type"`
				URL         string `yaml:"url"`
				Interval    uint64 `yaml:"interval"`
				HealthCheck struct {
					Enable   bool   `yaml:"enable"`
					URL      string `yaml:"url"`
					Interval uint64 `yaml:"interval"`
					Lazy     bool   `yaml:"lazy"`
				} `yaml:"health-check"`
			}
			if provider.Decode(&parsed) != nil {
				return missing("provider 字段类型无效")
			}
			if parsed.Type == "http" {
				if _, err := normalizeSubscriptionURL(parsed.URL); err != nil {
					return missing("provider URL 无效")
				}
			}
			if parsed.HealthCheck.Enable && parsed.HealthCheck.URL != "" {
				if _, err := normalizeSubscriptionURL(parsed.HealthCheck.URL); err != nil {
					return missing("provider health-check URL 无效")
				}
			}
			typ := mappingValue(provider, "type")
			if typ == nil || typ.Kind != yaml.ScalarNode {
				return missing("provider 类型缺失")
			}
			if typ.Value == "inline" {
				continue
			}
			if typ.Value != "http" && typ.Value != "file" {
				return missing("provider 类型不支持离线校验")
			}
			path := mappingValue(provider, "path")
			if path == nil || path.Kind != yaml.ScalarNode {
				return missing("provider 缺少可校验的本地缓存路径")
			}
			data, err := readResource(path.Value)
			if err != nil {
				return missing("provider 本地缓存缺失或路径不安全")
			}
			resources[filepath.Clean(filepath.FromSlash(path.Value))] = data
			setScalar(provider, "type", "file")
			deleteMapping(provider, "url")
			deleteMapping(provider, "interval")
			deleteMapping(provider, "health-check")
		}
	}
	// Disable *every* Geo download route even when an existing file is corrupt. A
	// non-HTTP scheme fails before a transport can make a connection.
	geox := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, key := range []string{"geoip", "geosite", "mmdb", "asn"} {
		setScalar(geox, key, "offline://disabled")
	}
	setMapping(root, "geox-url", geox)
	setScalar(root, "geo-auto-update", "false", "!!bool")
	deleteMapping(root, "external-ui-url")
	needsSite, needsIP, needsASN := false, false, false
	var scan func(*yaml.Node)
	scan = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			value := strings.ToLower(n.Value)
			if strings.Contains(value, "geosite:") || strings.HasPrefix(value, "geosite,") {
				needsSite = true
			}
			if strings.Contains(value, "geoip:") || (strings.HasPrefix(value, "geoip,") && !strings.HasPrefix(value, "geoip,private,")) {
				needsIP = true
			}
			if strings.HasPrefix(value, "ip-asn,") {
				needsASN = true
			}
		}
		for _, child := range n.Content {
			scan(child)
		}
	}
	scan(root)
	for _, data := range resources {
		var node yaml.Node
		if yaml.Unmarshal(data, &node) == nil {
			scan(&node)
		}
	}
	if filter := mappingValue(root, "dns"); filter != nil {
		if fallback := mappingValue(filter, "fallback-filter"); fallback != nil {
			if enabled := mappingValue(fallback, "geoip"); enabled != nil && enabled.Value == "true" {
				needsIP = true
			}
		}
	}
	required := []string{}
	if needsSite {
		required = append(required, "GeoSite.dat")
	}
	if needsIP {
		name := "geoip.metadb"
		if mode := mappingValue(root, "geodata-mode"); mode != nil && mode.Value == "true" {
			name = "geoip.dat"
		}
		required = append(required, name)
	}
	if needsASN {
		required = append(required, "ASN.mmdb")
	}
	for _, name := range required {
		if !regularFileExists(filepath.Join(a.dataDir, name)) {
			return missing("所需本地 Geo 数据缺失")
		}
	}
	rendered, err := yaml.Marshal(root)
	return rendered, resources, err
}

func safeOverrideValidationError(err error) error {
	var unavailable *offlineValidationUnavailable
	if errors.As(err, &unavailable) {
		return unavailable
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("合并配置无效，请检查 YAML、策略引用及所需本地资源；不回显可能含秘密的内核错误")
}

func decodeOverrideRequest(r *http.Request, req *overrideRequest) error {
	if r.Header.Get("X-Mihomo-Lite") != "1" {
		return errors.New("请求校验失败，请刷新页面后重试")
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if rejectDuplicateJSONKeys(data) != nil || requireJSONFields(data, nil, []string{"scope", "profileId", "yaml"}) != nil {
		return errors.New("请求包含重复键或未知字段")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for _, value := range fields {
		if len(value) == 0 || value[0] != '"' {
			return errors.New("请求字段必须是字符串")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(req); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("请求包含额外 JSON 内容")
	}
	return nil
}

func (a *Application) Overrides(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		a.backupFiles.RLock()
		defer a.backupFiles.RUnlock()
		profiles, err := a.loadProfileStore()
		if err != nil {
			writeJSON(w, 500, errorResponse{Error: "无法读取配置列表"})
			return
		}
		scope, id := r.URL.Query().Get("scope"), r.URL.Query().Get("profileId")
		if err := overrideScope(scope, id, profiles); err != nil {
			writeJSON(w, 400, errorResponse{Error: err.Error()})
			return
		}
		store, err := a.loadOverrides()
		if err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		text := store.Global
		if scope == "profile" {
			text = store.Profiles[id]
		}
		simple, err := readOverrideSimple(text)
		if err != nil {
			writeJSON(w, 500, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"scope": scope, "profileId": id, "yaml": text, "simple": simple, "precedence": overridePrecedence(), "warning": "覆写可能包含节点密码等秘密，请勿公开分享。"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, 405, errorResponse{Error: "不支持的请求方法"})
		return
	}
	var req overrideRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxOverrideYAMLBytes*6)
	if err := decodeOverrideRequest(r, &req); err != nil || req.YAML == nil {
		writeJSON(w, 400, errorResponse{Error: "请提供 scope/profileId/yaml"})
		return
	}
	if err := a.begin(); err != nil {
		writeJSON(w, 409, errorResponse{Error: err.Error()})
		return
	}
	count, err := a.saveOverrides(r.Context(), req.Scope, req.ProfileID, *req.YAML)
	a.finish(err)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"saved": true, "applied": false, "requiresReconnect": true, "validatedProfiles": count, "precedence": overridePrecedence(), "message": "覆写已保存；未改写原订阅或运行配置，请明确重新连接以应用。"})
}

func (a *Application) ConfigPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, 405, errorResponse{Error: "不支持的请求方法"})
		return
	}
	var req overrideRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxOverrideYAMLBytes*6)
	if err := decodeOverrideRequest(r, &req); err != nil {
		writeJSON(w, 400, errorResponse{Error: "预览请求格式无效"})
		return
	}
	a.backupFiles.RLock()
	defer a.backupFiles.RUnlock()
	profiles, err := a.loadProfileStore()
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: "无法读取配置列表"})
		return
	}
	if profileIndex(profiles, req.ProfileID) < 0 {
		writeJSON(w, 400, errorResponse{Error: "请选择有效配置预览"})
		return
	}
	store, err := a.loadOverrides()
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: err.Error()})
		return
	}
	if req.YAML != nil {
		if req.Scope != "global" && req.Scope != "profile" {
			writeJSON(w, 400, errorResponse{Error: "候选预览 scope 必须为 global 或 profile"})
			return
		}
		store, err = withOverrideCandidate(store, req.Scope, req.ProfileID, *req.YAML)
		if err != nil {
			writeJSON(w, 400, errorResponse{Error: err.Error()})
			return
		}
	} else if req.Scope != "" && req.Scope != "global" && req.Scope != "profile" {
		writeJSON(w, 400, errorResponse{Error: "scope 必须为 global 或 profile"})
		return
	}
	content, err := os.ReadFile(a.profileContentPath(req.ProfileID))
	if err != nil {
		writeJSON(w, 500, errorResponse{Error: "无法读取目标配置"})
		return
	}
	_, rendered, err := a.prepareProfileConfig(content, req.ProfileID, a.previewRuntime(), nil, &store)
	if err != nil {
		writeJSON(w, 400, errorResponse{Error: safeOverrideValidationError(err).Error()})
		return
	}
	result := configPreviewResponse{ProfileID: req.ProfileID, YAML: string(rendered), Precedence: overridePrecedence(), Warning: overrideWarning, Validation: "官方内核隔离 -t 离线静态校验通过；仅使用已有本地资源，不代表节点连通性。自动端口为展示占位值，连接时重新选择。"}
	if err := a.testConfigOfflineContext(r.Context(), rendered); err != nil {
		result.Validation = safeOverrideValidationError(err).Error()
	} else {
		result.Validated = true
	}
	writeJSON(w, 200, result)
}
