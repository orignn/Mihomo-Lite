package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxBackupJSONBytes      = 64 << 20
	maxBackupProfiles       = 100
	maxBackupYAMLBytes      = 4 << 20
	maxBackupRules          = 1000
	maxBackupRulesBytes     = 1 << 20
	maxBackupJournalEntries = 700
	legacyBackupScope       = "导出包含已保存的配置、订阅元数据、自定义规则及存在的上一版 YAML；不包含本地设置（导入时仅同步当前配置名称）、API secret、代理状态、日志、缓存、浏览器草稿或已删除配置。"
	overrideBackupScope     = "导出包含已保存的配置、订阅元数据、自定义规则、独立全局/每配置覆写及存在的上一版 YAML；包含端口偏好（还原后下次程序启动生效）；不包含其它本地启动设置、API secret、代理状态、日志、缓存、浏览器草稿或已删除配置。"
	backupScope             = "导出包含已保存的配置、订阅元数据、自定义规则及存在的上一版 YAML；包含端口偏好（还原后下次程序启动生效）；不包含其它本地启动设置、API secret、代理状态、日志、缓存、浏览器草稿或已删除配置。"
)

var errInvalidBackup = errors.New("invalid backup")
var errBackupCommit = errors.New("backup transaction failed")

type backupUpdate struct {
	At     time.Time `json:"at"`
	Status string    `json:"status"`
	Auto   bool      `json:"auto"`
}
type backupProfile struct {
	ID              string                    `json:"id"`
	Name            string                    `json:"name"`
	Source          string                    `json:"source"`
	URL             string                    `json:"url,omitempty"`
	Traffic         *subscriptionTraffic      `json:"traffic,omitempty"`
	CreatedAt       string                    `json:"createdAt"`
	UpdatedAt       string                    `json:"updatedAt"`
	Size            int64                     `json:"size"`
	Active          bool                      `json:"active"`
	LastUpdate      *backupUpdate             `json:"lastUpdate,omitempty"`
	AutoUpdateHours int                       `json:"autoUpdateHours"`
	NextUpdateAt    string                    `json:"nextUpdateAt,omitempty"`
	YAML            string                    `json:"yaml"`
	SHA256          string                    `json:"sha256"`
	PreviousYAML    string                    `json:"previousYaml,omitempty"`
	PreviousSHA256  string                    `json:"previousSha256,omitempty"`
	UpdatePolicy    *subscriptionUpdatePolicy `json:"updatePolicy,omitempty"`
	UpdateRetry     *subscriptionUpdateRetry  `json:"updateRetry,omitempty"`
	TrafficScanned  bool                      `json:"trafficScanned,omitempty"`
}
type backupDocument struct {
	SchemaVersion int             `json:"schemaVersion"`
	Version       string          `json:"version"`
	ExportedAt    string          `json:"exportedAt"`
	Scope         string          `json:"scope"`
	ActiveID      string          `json:"activeId"`
	Profiles      []backupProfile `json:"profiles"`
	Rules         []CustomRule    `json:"rules"`
	Ports         *PortSettings   `json:"ports,omitempty"`
	Overrides     *overrideStore  `json:"overrides,omitempty"`
	Checksum      string          `json:"checksum,omitempty"`
}
type backupJournalEntry struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	SHA256  string `json:"sha256,omitempty"`
	Size    int64  `json:"size,omitempty"`
}
type backupJournal struct {
	Scoped  bool                 `json:"scoped,omitempty"`
	State   string               `json:"state"`
	Entries []backupJournalEntry `json:"entries"`
}
type backupFileChange struct {
	Path    string
	Content []byte
	Remove  bool
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (a *Application) Backup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	switch r.Method {
	case http.MethodGet:
		if err := a.begin(); err != nil {
			writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
			return
		}
		data, err := a.exportBackup()
		a.finish(backupSafeError(err))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "无法生成完整配置备份"})
			return
		}
		filename := "mihomolite-backup-" + time.Now().UTC().Format("20060102T150405Z") + ".json"
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case http.MethodPost:
		if err := a.begin(); err != nil {
			writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
			return
		}
		a.mu.Lock()
		running := a.core != nil
		a.mu.Unlock()
		if running {
			a.finish(nil)
			writeJSON(w, http.StatusConflict, errorResponse{Error: "请先断开连接后再还原备份"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBackupJSONBytes)
		data, readErr := io.ReadAll(r.Body)
		err := readErr
		recoverySnapshot := ""
		if err == nil {
			err = a.importBackup(data)
			if err == nil {
				recoverySnapshot = a.latestRecoverySnapshot()
			}
		}
		var response ProfileResponse
		if err == nil {
			response, err = a.profileResponse()
		}
		a.finish(backupSafeError(err))
		if err != nil {
			var maxErr *http.MaxBytesError
			status, message := http.StatusBadRequest, "备份文件无效或未通过安全校验；现有配置未更改"
			if errors.As(readErr, &maxErr) {
				status, message = http.StatusRequestEntityTooLarge, "备份文件超过 64 MiB 限制"
			} else if errors.Is(err, errBackupCommit) {
				status, message = http.StatusInternalServerError, "备份还原未完成；现有数据已恢复或保留了可恢复快照"
			}
			writeJSON(w, status, errorResponse{Error: message})
			return
		}
		writeJSON(w, http.StatusOK, struct {
			ProfileResponse
			Restored         bool   `json:"restored"`
			Scope            string `json:"scope"`
			RecoverySnapshot string `json:"recoverySnapshot"`
		}{ProfileResponse: response, Restored: true, Scope: backupScope, RecoverySnapshot: recoverySnapshot})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "不支持的请求方法"})
	}
}

func backupSafeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errBackupCommit) {
		return errors.New("配置备份事务失败，已尝试恢复原有数据")
	}
	return errors.New("备份无效或未通过安全校验，现有配置未更改")
}

func (a *Application) exportBackup() ([]byte, error) {
	store, err := a.loadProfileStore()
	if err != nil || len(store.Profiles) > maxBackupProfiles {
		return nil, errInvalidBackup
	}
	rules, err := a.loadCustomRules()
	if err != nil || len(rules) > maxBackupRules {
		return nil, errInvalidBackup
	}
	if rules == nil {
		rules = []CustomRule{}
	}
	rb, err := json.Marshal(rules)
	if err != nil || len(rb) > maxBackupRulesBytes {
		return nil, errInvalidBackup
	}
	doc := backupDocument{SchemaVersion: 1, Version: AppVersion, ExportedAt: time.Now().UTC().Format(time.RFC3339Nano), Scope: overrideBackupScope, ActiveID: store.ActiveID, Profiles: make([]backupProfile, 0, len(store.Profiles)), Rules: rules}
	a.mu.Lock()
	ports := a.configuredPortsLocked()
	a.mu.Unlock()
	doc.Ports = &ports
	overrides, err := a.loadOverrides()
	if err != nil {
		return nil, errInvalidBackup
	}
	profileIDs := map[string]bool{}
	for _, p := range store.Profiles {
		profileIDs[p.ID] = true
	}
	// Deleted profile layers are retained locally but are not part of the live configuration backup.
	for id := range overrides.Profiles {
		if !profileIDs[id] {
			delete(overrides.Profiles, id)
		}
	}
	if overrides.Global != "" || len(overrides.Profiles) > 0 {
		doc.Overrides = &overrides
	}
	for _, p := range store.Profiles {
		content, err := os.ReadFile(a.profileContentPath(p.ID))
		if err != nil || len(content) == 0 || len(content) > maxBackupYAMLBytes || !utf8.Valid(content) {
			return nil, errInvalidBackup
		}
		item := backupProfile{ID: p.ID, Name: p.Name, Source: p.Source, URL: p.URL, Traffic: p.Traffic, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Size: int64(len(content)), Active: p.ID == store.ActiveID, AutoUpdateHours: p.AutoUpdateHours, NextUpdateAt: p.NextUpdateAt, UpdatePolicy: p.UpdatePolicy, UpdateRetry: p.UpdateRetry, TrafficScanned: p.TrafficScanned, YAML: string(content), SHA256: sha256Hex(content)}
		if p.LastUpdate != nil {
			item.LastUpdate = &backupUpdate{At: p.LastUpdate.At, Status: p.LastUpdate.Status, Auto: p.LastUpdate.Auto}
		}
		prior, err := os.ReadFile(a.profileContentPath(p.ID) + ".bak")
		if err == nil {
			if len(prior) == 0 || len(prior) > maxBackupYAMLBytes || !utf8.Valid(prior) {
				return nil, errInvalidBackup
			}
			item.PreviousYAML = string(prior)
			item.PreviousSHA256 = sha256Hex(prior)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, errInvalidBackup
		}
		doc.Profiles = append(doc.Profiles, item)
	}
	if len(doc.Profiles) > 0 && !validProfileID(doc.ActiveID) {
		return nil, errInvalidBackup
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, errInvalidBackup
	}
	doc.Checksum = sha256Hex(raw)
	output, err := json.MarshalIndent(doc, "", "  ")
	if err != nil || len(output)+1 > maxBackupJSONBytes {
		return nil, errInvalidBackup
	}
	return append(output, '\n'), nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errInvalidBackup
				}
				seen[key] = true
				if err := value(); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return errInvalidBackup
			}
		case '[':
			for dec.More() {
				if err := value(); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return errInvalidBackup
			}
		default:
			return errInvalidBackup
		}
		return nil
	}
	if value() != nil {
		return errInvalidBackup
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errInvalidBackup
	}
	return nil
}

func requireJSONFields(raw json.RawMessage, required, optional []string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errInvalidBackup
	}
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
		if _, ok := fields[key]; !ok {
			return errInvalidBackup
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return errInvalidBackup
		}
	}
	return nil
}

func decodeBackupDocument(data []byte) (backupDocument, error) {
	var doc backupDocument
	if len(data) == 0 || len(data) > maxBackupJSONBytes || rejectDuplicateJSONKeys(data) != nil {
		return doc, errInvalidBackup
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return doc, errInvalidBackup
	}
	raw, _ := json.Marshal(fields)
	if requireJSONFields(raw, []string{"schemaVersion", "version", "exportedAt", "scope", "activeId", "profiles", "rules", "checksum"}, []string{"ports", "overrides"}) != nil {
		return doc, errInvalidBackup
	}
	if rawOverrides, present := fields["overrides"]; present {
		if requireJSONFields(rawOverrides, []string{"schemaVersion", "global", "profiles"}, nil) != nil {
			return doc, errInvalidBackup
		}
		var candidate overrideStore
		if json.Unmarshal(rawOverrides, &candidate) != nil {
			return doc, errInvalidBackup
		}
		if _, err := normalizeOverrideStore(&candidate); err != nil {
			return doc, errInvalidBackup
		}
	}
	if ports, present := fields["ports"]; present {
		if requireJSONFields(ports, []string{"proxy", "backend", "panel", "web"}, nil) != nil {
			return doc, errInvalidBackup
		}
		var options map[string]json.RawMessage
		_ = json.Unmarshal(ports, &options)
		for _, option := range options {
			if requireJSONFields(option, []string{"mode", "port"}, nil) != nil {
				return doc, errInvalidBackup
			}
		}
		var candidate PortSettings
		if json.Unmarshal(ports, &candidate) != nil {
			return doc, errInvalidBackup
		}
		if _, err := normalizePortSettings(candidate); err != nil {
			return doc, errInvalidBackup
		}
	}
	trimmedProfiles, trimmedRules := bytes.TrimSpace(fields["profiles"]), bytes.TrimSpace(fields["rules"])
	if len(trimmedProfiles) == 0 || trimmedProfiles[0] != '[' || len(trimmedRules) == 0 || trimmedRules[0] != '[' {
		return doc, errInvalidBackup
	}
	var ps []json.RawMessage
	if json.Unmarshal(fields["profiles"], &ps) != nil || len(ps) > maxBackupProfiles {
		return doc, errInvalidBackup
	}
	for _, raw := range ps {
		if requireJSONFields(raw, []string{"id", "name", "source", "createdAt", "updatedAt", "size", "active", "autoUpdateHours", "yaml", "sha256"}, []string{"url", "traffic", "lastUpdate", "nextUpdateAt", "previousYaml", "previousSha256", "updatePolicy", "updateRetry", "trafficScanned"}) != nil {
			return doc, errInvalidBackup
		}
		var f map[string]json.RawMessage
		_ = json.Unmarshal(raw, &f)
		if traffic, ok := f["traffic"]; ok && string(traffic) != "null" && requireJSONFields(traffic, []string{"checkedAt"}, []string{"upload", "download", "total", "expire", "remaining", "used", "source", "expireDate"}) != nil {
			return doc, errInvalidBackup
		}
		if p, ok := f["updatePolicy"]; ok && requireJSONFields(p, []string{"mode", "conditional", "failureBackoff"}, []string{"userAgent"}) != nil {
			return doc, errInvalidBackup
		}
		if retry, ok := f["updateRetry"]; ok && requireJSONFields(retry, []string{"consecutiveFailures", "lastFailureAt", "retryAt"}, nil) != nil {
			return doc, errInvalidBackup
		}
		if u, ok := f["lastUpdate"]; ok && string(u) != "null" && requireJSONFields(u, []string{"at", "status", "auto"}, nil) != nil {
			return doc, errInvalidBackup
		}
	}
	var rs []json.RawMessage
	if json.Unmarshal(fields["rules"], &rs) != nil || len(rs) > maxBackupRules {
		return doc, errInvalidBackup
	}
	for _, raw := range rs {
		if requireJSONFields(raw, []string{"id", "type", "value", "policy", "enabled"}, []string{"note", "noResolve"}) != nil {
			return doc, errInvalidBackup
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&doc) != nil {
		return doc, errInvalidBackup
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		return doc, errInvalidBackup
	}
	if doc.SchemaVersion != 1 || doc.Version == "" || (doc.Scope != overrideBackupScope && doc.Scope != backupScope && (doc.Scope != legacyBackupScope || doc.Ports != nil)) || len(doc.Profiles) > maxBackupProfiles || len(doc.Rules) > maxBackupRules {
		return doc, errInvalidBackup
	}
	if _, err := time.Parse(time.RFC3339Nano, doc.ExportedAt); err != nil {
		return doc, errInvalidBackup
	}
	checksum := doc.Checksum
	if len(checksum) != 64 || strings.ToLower(checksum) != checksum {
		return doc, errInvalidBackup
	}
	want, err := hex.DecodeString(checksum)
	if err != nil {
		return doc, errInvalidBackup
	}
	doc.Checksum = ""
	canonical, err := json.Marshal(doc)
	if err != nil {
		return doc, errInvalidBackup
	}
	actual := sha256.Sum256(canonical)
	if subtle.ConstantTimeCompare(want, actual[:]) != 1 {
		return doc, errInvalidBackup
	}
	doc.Checksum = checksum
	return doc, nil
}

func validateBackupRules(rules []CustomRule) ([]CustomRule, error) {
	if len(rules) > maxBackupRules {
		return nil, errInvalidBackup
	}
	clean := append([]CustomRule(nil), rules...)
	ids, dups := map[string]bool{}, map[string]bool{}
	for i := range clean {
		original := clean[i]
		if original.ID == "" || len(original.ID) > 100 || strings.ContainsAny(original.ID, "/\\\r\n\x00") || ids[original.ID] {
			return nil, errInvalidBackup
		}
		ids[original.ID] = true
		if validateCustomRule(&clean[i]) != nil || clean[i] != original {
			return nil, errInvalidBackup
		}
		key := original.Type + "\x00" + original.Value + "\x00" + original.Policy
		if dups[key] {
			return nil, errInvalidBackup
		}
		dups[key] = true
	}
	raw, err := json.Marshal(clean)
	if err != nil || len(raw) > maxBackupRulesBytes {
		return nil, errInvalidBackup
	}
	return clean, nil
}

func validateBackupProfile(p backupProfile, active string) (Profile, []byte, []byte, error) {
	var out Profile
	if !validProfileID(p.ID) || strings.ToLower(p.ID) != p.ID || p.Active != (p.ID == active) || p.Size < 1 || p.Size > maxBackupYAMLBytes || int64(len(p.YAML)) != p.Size || !utf8.ValidString(p.YAML) || sha256Hex([]byte(p.YAML)) != p.SHA256 {
		return out, nil, nil, errInvalidBackup
	}
	name, err := normalizeProfileName(p.Name)
	if err != nil || name != p.Name {
		return out, nil, nil, errInvalidBackup
	}
	if _, err := time.Parse(time.RFC3339, p.CreatedAt); err != nil {
		return out, nil, nil, errInvalidBackup
	}
	if _, err := time.Parse(time.RFC3339, p.UpdatedAt); err != nil {
		return out, nil, nil, errInvalidBackup
	}
	if p.Source != "local" && p.Source != "subscription" {
		return out, nil, nil, errInvalidBackup
	}
	if !validSubscriptionTraffic(p.Traffic) {
		return out, nil, nil, errInvalidBackup
	}
	if p.Source == "local" {
		if p.URL != "" || p.Traffic != nil || p.TrafficScanned || p.AutoUpdateHours != 0 || p.NextUpdateAt != "" {
			return out, nil, nil, errInvalidBackup
		}
	} else {
		address, err := normalizeSubscriptionURL(p.URL)
		if err != nil || address != p.URL {
			return out, nil, nil, errInvalidBackup
		}
		if !validSubscriptionAutoHours(p.AutoUpdateHours) {
			return out, nil, nil, errInvalidBackup
		}
		if p.AutoUpdateHours == 0 && p.NextUpdateAt != "" {
			return out, nil, nil, errInvalidBackup
		}
		if p.AutoUpdateHours > 0 {
			if _, err := time.Parse(time.RFC3339Nano, p.NextUpdateAt); err != nil {
				return out, nil, nil, errInvalidBackup
			}
		}
	}
	var last *subscriptionUpdate
	if p.LastUpdate != nil {
		if p.Source != "subscription" || p.LastUpdate.At.IsZero() || (p.LastUpdate.Status != "success" && p.LastUpdate.Status != "unchanged" && p.LastUpdate.Status != "failed" && p.LastUpdate.Status != "saved") {
			return out, nil, nil, errInvalidBackup
		}
		last = &subscriptionUpdate{At: p.LastUpdate.At, Status: p.LastUpdate.Status, Auto: p.LastUpdate.Auto}
	}
	var previous []byte
	if p.PreviousYAML == "" {
		if p.PreviousSHA256 != "" {
			return out, nil, nil, errInvalidBackup
		}
	} else {
		if len(p.PreviousYAML) > maxBackupYAMLBytes || !utf8.ValidString(p.PreviousYAML) || p.PreviousSHA256 == "" || sha256Hex([]byte(p.PreviousYAML)) != p.PreviousSHA256 {
			return out, nil, nil, errInvalidBackup
		}
		previous = []byte(p.PreviousYAML)
	}
	out = Profile{ID: p.ID, Name: p.Name, Source: p.Source, URL: p.URL, Traffic: p.Traffic, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Size: p.Size, Active: p.Active, LastUpdate: last, AutoUpdateHours: p.AutoUpdateHours, NextUpdateAt: p.NextUpdateAt, UpdatePolicy: p.UpdatePolicy, UpdateRetry: p.UpdateRetry, TrafficScanned: p.TrafficScanned}
	if !validSubscriptionProfileMetadata(out) {
		return Profile{}, nil, nil, errInvalidBackup
	}
	return out, []byte(p.YAML), previous, nil
}

func checkBackupResourcePaths(content []byte) error {
	var doc yaml.Node
	if yaml.Unmarshal(content, &doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errInvalidBackup
	}
	root, err := materializeConfigRoot(doc.Content[0])
	if err != nil {
		return errInvalidBackup
	}
	for _, section := range []string{"proxy-providers", "rule-providers"} {
		providers := mappingValue(root, section)
		if providers == nil {
			continue
		}
		if providers.Kind != yaml.MappingNode {
			return errInvalidBackup
		}
		for index := 1; index < len(providers.Content); index += 2 {
			provider := providers.Content[index]
			if provider.Kind != yaml.MappingNode {
				return errInvalidBackup
			}
			if field := mappingValue(provider, "path"); field != nil {
				path := strings.TrimSpace(field.Value)
				if field.Kind != yaml.ScalarNode || path == "" || filepath.IsAbs(path) || strings.Contains(path, ":") || strings.HasPrefix(path, "\\") {
					return errInvalidBackup
				}
				for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
					if part == ".." || part == "~" {
						return errInvalidBackup
					}
				}
			}
			if field := mappingValue(provider, "url"); field != nil {
				address, err := url.Parse(field.Value)
				if field.Kind != yaml.ScalarNode || err != nil || address.User != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
					return errInvalidBackup
				}
			}
		}
	}
	return nil
}

func (a *Application) validateBackupYAML(content []byte, rules []CustomRule) error {
	empty := emptyOverrideStore()
	return a.validateBackupYAMLWithOverrides(content, "", rules, &empty)
}

func (a *Application) validateBackupYAMLWithOverrides(content []byte, id string, rules []CustomRule, overrides *overrideStore) error {
	if len(content) == 0 || len(content) > maxBackupYAMLBytes || !utf8.Valid(content) || checkBackupResourcePaths(content) != nil {
		return errInvalidBackup
	}
	var doc yaml.Node
	if yaml.Unmarshal(content, &doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errInvalidBackup
	}
	root, err := materializeConfigRoot(doc.Content[0])
	if err != nil || (mappingValue(root, "proxies") == nil && mappingValue(root, "proxy-providers") == nil) {
		return errInvalidBackup
	}
	_, rendered, err := a.prepareProfileConfig(content, id, RuntimeConfig{}, rules, overrides)
	if err != nil || a.testConfigOfflineContext(context.Background(), rendered) != nil {
		return errInvalidBackup
	}
	return nil
}

func (a *Application) importBackup(data []byte) error {
	doc, err := decodeBackupDocument(data)
	if err != nil {
		return errInvalidBackup
	}
	rules, err := validateBackupRules(doc.Rules)
	if err != nil {
		return errInvalidBackup
	}
	overrides, err := normalizeOverrideStore(doc.Overrides)
	if err != nil {
		return errInvalidBackup
	}
	for id := range overrides.Profiles {
		found := false
		for _, item := range doc.Profiles {
			if item.ID == id {
				found = true
				break
			}
		}
		if !found {
			return errInvalidBackup
		}
	}
	seen := map[string]bool{}
	profiles := make([]Profile, 0, len(doc.Profiles))
	contents, previous := map[string][]byte{}, map[string][]byte{}
	var total int64
	for _, item := range doc.Profiles {
		if seen[item.ID] {
			return errInvalidBackup
		}
		seen[item.ID] = true
		p, content, prior, err := validateBackupProfile(item, doc.ActiveID)
		if err != nil {
			return errInvalidBackup
		}
		total += int64(len(content) + len(prior))
		if total > maxBackupJSONBytes {
			return errInvalidBackup
		}
		candidateRules := []CustomRule{}
		if p.ID == doc.ActiveID {
			candidateRules = rules
		}
		if a.validateBackupYAMLWithOverrides(content, p.ID, candidateRules, &overrides) != nil {
			return errInvalidBackup
		}
		if len(prior) > 0 && a.validateBackupYAMLWithOverrides(prior, p.ID, []CustomRule{}, &overrides) != nil {
			return errInvalidBackup
		}
		profiles = append(profiles, p)
		contents[p.ID] = content
		if len(prior) > 0 {
			previous[p.ID] = prior
		}
	}
	if len(profiles) == 0 {
		if doc.ActiveID != "" {
			return errInvalidBackup
		}
	} else if !seen[doc.ActiveID] {
		return errInvalidBackup
	}
	clean, err := validateBackupRules(rules)
	if err != nil {
		return errInvalidBackup
	}
	store := profileStore{ActiveID: doc.ActiveID, Profiles: profiles}
	for i := range store.Profiles {
		store.Profiles[i].Active = store.Profiles[i].ID == store.ActiveID
		store.Profiles[i].Pending = false
		store.Profiles[i].HasUpdateDiff = false
	}
	storeBytes, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return errInvalidBackup
	}
	storeBytes = append(storeBytes, '\n')
	ruleBytes, err := json.MarshalIndent(clean, "", "  ")
	if err != nil || len(ruleBytes) > maxBackupRulesBytes {
		return errInvalidBackup
	}
	ruleBytes = append(ruleBytes, '\n')
	overrideBytes, err := json.MarshalIndent(overrides, "", "  ")
	if err != nil {
		return errInvalidBackup
	}
	changes := map[string]backupFileChange{}
	managed, err := managedProfilePaths(a.dataDir)
	if err != nil {
		return errInvalidBackup
	}
	for _, path := range managed {
		changes[path] = backupFileChange{Path: path, Remove: true}
	}
	for _, p := range profiles {
		rel := filepath.ToSlash(filepath.Join("profiles", p.ID+".yaml"))
		changes[rel] = backupFileChange{Path: rel, Content: contents[p.ID]}
		bak := rel + ".bak"
		if prior, ok := previous[p.ID]; ok {
			changes[bak] = backupFileChange{Path: bak, Content: prior}
		} else {
			changes[bak] = backupFileChange{Path: bak, Remove: true}
		}
		diff := filepath.ToSlash(filepath.Join("profiles", p.ID+".subscription-diff.json"))
		changes[diff] = backupFileChange{Path: diff, Remove: true}
	}
	ordered := make([]backupFileChange, 0, len(changes)+6)
	for _, c := range changes {
		ordered = append(ordered, c)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	ordered = append(ordered, backupFileChange{Path: "profiles.json", Content: storeBytes}, backupFileChange{Path: "custom-rules.json", Content: ruleBytes}, backupFileChange{Path: "overrides.json", Content: overrideBytes}, backupFileChange{Path: "pending-profile", Remove: true})
	if doc.ActiveID == "" {
		ordered = append(ordered, backupFileChange{Path: "source.yaml", Remove: true})
	} else {
		ordered = append(ordered, backupFileChange{Path: "source.yaml", Content: contents[doc.ActiveID]})
	}
	name := ""
	for _, p := range profiles {
		if p.ID == doc.ActiveID {
			name = p.Name
			break
		}
	}
	settings, err := a.settingsWithProfileName(name)
	if err != nil {
		return errInvalidBackup
	}
	var pendingSettings Settings
	if json.Unmarshal(settings, &pendingSettings) != nil {
		return errInvalidBackup
	}
	if doc.Ports != nil {
		pendingSettings.Ports, err = normalizePortSettings(*doc.Ports)
		if err != nil {
			return errInvalidBackup
		}
		settings, err = json.MarshalIndent(pendingSettings, "", "  ")
		if err != nil {
			return errInvalidBackup
		}
	}
	ordered = append(ordered, backupFileChange{Path: "@launcher.json", Content: settings})
	if err := a.commitBackupFiles(ordered); err != nil {
		return errBackupCommit
	}
	a.mu.Lock()
	if a.config != nil {
		a.activePortsLocked()
		a.config.ConfigName = name
		if doc.Ports != nil {
			a.config.Ports = pendingSettings.Ports
		}
	}
	a.mu.Unlock()
	a.profileRevision.Add(1)
	return nil
}

func (a *Application) settingsWithProfileName(name string) ([]byte, error) {
	settings := Settings{}
	path := filepath.Join(a.baseDir, "launcher.json")
	raw, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(raw, &settings) != nil {
			return nil, errInvalidBackup
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if a.config != nil {
			settings = *a.config
		}
	} else {
		return nil, err
	}
	settings.ConfigName = name
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func managedProfilePaths(dataDir string) ([]string, error) {
	dir := filepath.Join(dataDir, "profiles")
	if err := checkBackupPath(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		for _, suffix := range []string{".yaml", ".yaml.bak", ".subscription-diff.json"} {
			id := strings.TrimSuffix(name, suffix)
			if id != name && validProfileID(id) {
				rel := "profiles/" + name
				if err := ensureBackupTarget(dataDir, rel); err != nil {
					return nil, err
				}
				paths = append(paths, rel)
				break
			}
		}
	}
	return paths, nil
}
func validBackupRelativePath(path string) bool {
	if filepath.IsAbs(path) || strings.Contains(path, "..") || strings.Contains(path, "\\") {
		return false
	}
	switch path {
	case "@launcher.json", "profiles.json", "source.yaml", "custom-rules.json", "overrides.json", "pending-profile":
		return true
	}
	if !strings.HasPrefix(path, "profiles/") {
		return false
	}
	name := strings.TrimPrefix(path, "profiles/")
	for _, suffix := range []string{".yaml", ".yaml.bak", ".subscription-diff.json"} {
		id := strings.TrimSuffix(name, suffix)
		if id != name && validProfileID(id) {
			return true
		}
	}
	return false
}
func backupTarget(dataDir, rel string) string {
	if rel == "@launcher.json" {
		return filepath.Join(filepath.Dir(dataDir), "launcher.json")
	}
	return filepath.Join(dataDir, filepath.FromSlash(rel))
}

var backupReplaceFile = replaceFile

func (a *Application) commitBackupFiles(changes []backupFileChange) error {
	return a.commitManagedFiles(changes, true)
}

// Scheduled content updates keep a scoped recovery journal, not a full export
// of every subscription. The same checked targets and startup recovery apply.
func (a *Application) commitSubscriptionFiles(changes []backupFileChange) error {
	return a.commitManagedFiles(changes, false)
}

func scopedSubscriptionPath(path, id string) bool {
	switch path {
	case "profiles.json", "source.yaml", "pending-profile", "profiles/" + id + ".yaml", "profiles/" + id + ".yaml.bak", "profiles/" + id + ".subscription-diff.json":
		return true
	}
	return false
}

func scopedSubscriptionID(changes []backupFileChange) (string, error) {
	id := ""
	for _, change := range changes {
		if strings.HasPrefix(change.Path, "profiles/") && strings.HasSuffix(change.Path, ".yaml") {
			candidate := strings.TrimSuffix(strings.TrimPrefix(change.Path, "profiles/"), ".yaml")
			if !validProfileID(candidate) || id != "" {
				return "", errInvalidBackup
			}
			id = candidate
		}
	}
	if id == "" {
		return "", errInvalidBackup
	}
	for _, change := range changes {
		if !scopedSubscriptionPath(change.Path, id) {
			return "", errInvalidBackup
		}
	}
	return id, nil
}

func decodeManagedJournal(raw []byte, directory string) (backupJournal, error) {
	var journal backupJournal
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 1<<20 || decoder.Decode(&journal) != nil || decoder.Decode(new(any)) != io.EOF || len(journal.Entries) > maxBackupJournalEntries {
		return journal, errInvalidBackup
	}
	if len(journal.Entries) == 0 && !(journal.Scoped && journal.State == "staging") {
		return journal, errInvalidBackup
	}
	if journal.Scoped {
		id := strings.TrimPrefix(filepath.Base(directory), "snapshot-subscription-")
		if !validProfileID(id) || filepath.Base(directory) != "snapshot-subscription-"+id {
			return journal, errInvalidBackup
		}
		seen := map[string]bool{}
		for _, entry := range journal.Entries {
			if !scopedSubscriptionPath(entry.Path, id) || seen[entry.Path] {
				return journal, errInvalidBackup
			}
			seen[entry.Path] = true
		}
	}
	switch journal.State {
	case "prepared", "committed", "rolled-back":
	case "staging":
		if !journal.Scoped {
			return journal, errInvalidBackup
		}
	default:
		return journal, errInvalidBackup
	}
	return journal, nil
}

func (a *Application) commitManagedFiles(changes []backupFileChange, completeSnapshot bool) error {
	if len(changes) == 0 {
		return nil
	}
	if len(changes) > maxBackupJournalEntries {
		return errInvalidBackup
	}
	a.backupFiles.Lock()
	defer a.backupFiles.Unlock()
	if err := checkBackupPath(a.dataDir); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, change := range changes {
		if seen[change.Path] || ensureBackupTarget(a.dataDir, change.Path) != nil {
			return errInvalidBackup
		}
		seen[change.Path] = true
	}
	// Manual destructive operations retain a complete importable snapshot.
	// Routine updates only retain the changed-file recovery images below.
	var before []byte
	if completeSnapshot {
		var err error
		before, err = a.exportBackup()
		if err != nil {
			return err
		}
	}
	stage, err := os.MkdirTemp(a.dataDir, ".backup-import-staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, change := range changes {
		if !change.Remove {
			if err := atomicWrite(filepath.Join(stage, filepath.FromSlash(change.Path)), change.Content, 0o600); err != nil {
				return err
			}
		}
	}
	root := filepath.Join(a.dataDir, "backup-import-recovery")
	if err := checkBackupPath(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	var snapshot string
	if completeSnapshot {
		snapshot, err = os.MkdirTemp(root, "snapshot-"+time.Now().UTC().Format("20060102T150405.000000000Z")+"-")
		if err != nil {
			return err
		}
	} else {
		id, idErr := scopedSubscriptionID(changes)
		if idErr != nil {
			return idErr
		}
		snapshot = filepath.Join(root, "snapshot-subscription-"+id)
		if checkBackupPath(snapshot) != nil {
			return errInvalidBackup
		}
		journalPath := filepath.Join(snapshot, "journal.json")
		if checkBackupPath(journalPath) != nil {
			return errInvalidBackup
		}
		previous, readErr := os.ReadFile(journalPath)
		if readErr == nil {
			old, decodeErr := decodeManagedJournal(previous, snapshot)
			if decodeErr != nil || !old.Scoped {
				return errInvalidBackup
			}
			if old.State == "prepared" {
				a.mu.Lock()
				a.backupRecoveryBlocked = true
				a.mu.Unlock()
				return errBackupCommit
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if err := os.MkdirAll(snapshot, 0o700); err != nil {
			return err
		}
		// Retire only a finished scoped transaction before reusing its fixed files.
		// Staging cannot have changed targets; a crash here must not replay old images.
		if err := writeBackupJournal(snapshot, backupJournal{Scoped: true, State: "staging", Entries: []backupJournalEntry{}}); err != nil {
			return err
		}
	}
	if completeSnapshot {
		if err := atomicWrite(filepath.Join(snapshot, "backup.json"), before, 0o600); err != nil {
			return err
		}
	}
	journal := backupJournal{Scoped: !completeSnapshot, State: "prepared", Entries: make([]backupJournalEntry, 0, len(changes))}
	for _, change := range changes {
		target := backupTarget(a.dataDir, change.Path)
		entry := backupJournalEntry{Path: change.Path}
		info, readErr := os.Lstat(target)
		if readErr == nil {
			if !info.Mode().IsRegular() || info.Size() > 16<<20 {
				return errInvalidBackup
			}
			raw, readErr := os.ReadFile(target)
			if readErr != nil {
				return readErr
			}
			if err := atomicWrite(filepath.Join(snapshot, "files", filepath.FromSlash(change.Path)), raw, 0o600); err != nil {
				return err
			}
			entry.Existed = true
			entry.SHA256 = sha256Hex(raw)
			entry.Size = int64(len(raw))
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		journal.Entries = append(journal.Entries, entry)
	}
	if err := writeBackupJournal(snapshot, journal); err != nil {
		return err
	}
	rollback := func() error {
		restoreErr := restoreBackupJournal(a.dataDir, snapshot, journal)
		if restoreErr == nil {
			journal.State = "rolled-back"
			restoreErr = writeBackupJournal(snapshot, journal)
		}
		if restoreErr != nil {
			a.mu.Lock()
			a.backupRecoveryBlocked = true
			a.mu.Unlock()
		}
		return errBackupCommit
	}
	for _, change := range changes {
		if err := ensureBackupTarget(a.dataDir, change.Path); err != nil {
			return rollback()
		}
		target := backupTarget(a.dataDir, change.Path)
		if change.Remove {
			err = os.Remove(target)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return rollback()
			}
			err = backupReplaceFile(filepath.Join(stage, filepath.FromSlash(change.Path)), target)
		}
		if err != nil {
			return rollback()
		}
	}
	journal.State = "committed"
	if err := writeBackupJournal(snapshot, journal); err != nil {
		return rollback()
	}
	if completeSnapshot {
		a.lastBackupSnapshot = filepath.ToSlash(filepath.Join("data", "backup-import-recovery", filepath.Base(snapshot), "backup.json"))
	}
	return nil
}
func (a *Application) latestRecoverySnapshot() string { return a.lastBackupSnapshot }
func ensureBackupTarget(dataDir, rel string) error {
	if !validBackupRelativePath(rel) {
		return errInvalidBackup
	}
	target := backupTarget(dataDir, rel)
	if err := checkBackupPath(target); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
		return errInvalidBackup
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func writeBackupJournal(dir string, journal backupJournal) error {
	if err := checkBackupPath(filepath.Join(dir, "journal.json")); err != nil {
		return err
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "journal.json"), raw, 0o600)
}
func restoreBackupJournal(dataDir, snapshot string, journal backupJournal) error {
	if len(journal.Entries) == 0 || len(journal.Entries) > maxBackupJournalEntries || checkBackupPath(snapshot) != nil {
		return errInvalidBackup
	}
	seen := map[string]bool{}
	originals := map[string][]byte{}
	// Validate every source first, so corrupt snapshots never cause partial restore.
	for _, entry := range journal.Entries {
		if seen[entry.Path] || ensureBackupTarget(dataDir, entry.Path) != nil {
			return errInvalidBackup
		}
		seen[entry.Path] = true
		if entry.Existed {
			source := filepath.Join(snapshot, "files", filepath.FromSlash(entry.Path))
			if checkBackupPath(source) != nil {
				return errInvalidBackup
			}
			info, err := os.Lstat(source)
			if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size || entry.Size > 16<<20 {
				return errInvalidBackup
			}
			raw, err := os.ReadFile(source)
			if err != nil || sha256Hex(raw) != entry.SHA256 {
				return errInvalidBackup
			}
			originals[entry.Path] = raw
		}
	}
	var result error
	for index := len(journal.Entries) - 1; index >= 0; index-- {
		entry := journal.Entries[index]
		target := backupTarget(dataDir, entry.Path)
		if err := ensureBackupTarget(dataDir, entry.Path); err != nil {
			result = errors.Join(result, err)
			continue
		}
		if entry.Existed {
			if err := atomicWrite(target, originals[entry.Path], 0o600); err != nil {
				result = errors.Join(result, err)
			}
		} else if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}
func (a *Application) recoverInterruptedBackupImports() error {
	root := filepath.Join(a.dataDir, "backup-import-recovery")
	if checkBackupPath(root) != nil {
		return errors.New("备份事务恢复目录路径无效")
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("无法读取备份事务恢复目录")
	}
	// Timestamp names sort in commit order. Never replay more than one outstanding
	// transaction: failed recovery blocks further writes within the same process.
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "snapshot-") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if checkBackupPath(dir) != nil || !entry.IsDir() {
			return errors.New("备份事务恢复目录路径无效")
		}
		path := filepath.Join(dir, "journal.json")
		if checkBackupPath(path) != nil {
			return errors.New("备份事务恢复记录路径无效")
		}
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || len(raw) > 1<<20 {
			return errors.New("无法读取备份事务恢复记录")
		}
		journal, decodeErr := decodeManagedJournal(raw, dir)
		if decodeErr != nil {
			return errors.New("备份事务恢复记录损坏")
		}
		switch journal.State {
		case "prepared":
			if restoreBackupJournal(a.dataDir, dir, journal) != nil {
				return errors.New("备份事务恢复失败；为保护数据已停止启动")
			}
			journal.State = "rolled-back"
			if writeBackupJournal(dir, journal) != nil {
				return errors.New("备份事务恢复状态无法保存")
			}
		case "committed", "rolled-back", "staging":
		default:
			return errors.New("备份事务状态无效；为保护数据已停止启动")
		}
	}
	return nil
}
