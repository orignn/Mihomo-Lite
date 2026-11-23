package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"gopkg.in/yaml.v3"
)

type CustomRule struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	Policy    string `json:"policy"`
	Enabled   bool   `json:"enabled"`
	Note      string `json:"note,omitempty"`
	NoResolve bool   `json:"noResolve,omitempty"`
}

type RulesResponse struct {
	Rules         []CustomRule `json:"rules"`
	Policies      []string     `json:"policies"`
	Running       bool         `json:"running"`
	UnifiedPolicy string       `json:"unifiedPolicy"`
}

type PolicyInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

var domainPattern = regexp.MustCompile(`^[A-Za-z0-9_](?:[A-Za-z0-9_.-]*[A-Za-z0-9_])?$`)
var processNamePattern = regexp.MustCompile(`^[^\\/:*?"<>|\x00-\x1f]+$`)

func (a *Application) Rules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rules, err := a.loadCustomRules()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		a.mu.Lock()
		runtime, running := a.runtime, a.core != nil
		a.mu.Unlock()
		policies := []PolicyInfo{{Name: unifiedProxyName, Type: "Selector"}, {Name: "DIRECT"}, {Name: "REJECT"}}
		if running {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if current, err := a.fetchPolicies(ctx, runtime); err == nil {
				policies = current
			}
		}
		writeJSON(w, http.StatusOK, RulesResponse{Rules: rules, Policies: policyNames(policies), Running: running, UnifiedPolicy: unifiedProxyName})
	case http.MethodPut:
		if err := a.begin(); err != nil {
			writeJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
			return
		}
		var req struct {
			Rules []CustomRule `json:"rules"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		err := decodeJSON(r, &req)
		if err == nil {
			err = a.saveCustomRules(context.WithoutCancel(r.Context()), req.Rules)
		}
		a.finish(err)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"saved": true, "applied": a.State().Running})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "不支持的请求方法"})
	}
}

type RuleTestResult struct {
	Query            string      `json:"query"`
	Kind             string      `json:"kind"`
	Status           string      `json:"status"` // matched, no-match, or unknown
	Matched          *CustomRule `json:"matched,omitempty"`
	PossibleMatch    *CustomRule `json:"possibleMatch,omitempty"`
	UncertainTypes   []string    `json:"uncertainTypes,omitempty"`
	MissingDatabases []string    `json:"missingDatabases,omitempty"`
}

func (a *Application) RuleTest(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请输入要测试的域名、IP 或进程名"})
		return
	}
	if utf8.RuneCountInString(query) > 2048 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "测试内容不能超过 2048 个字符"})
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "destination"
	}
	if kind != "destination" && kind != "process" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "测试类型必须是目标地址或进程"})
		return
	}
	rules, err := a.loadCustomRules()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	result, err := simulateCustomRules(rules, query, kind)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if result.Status == "unknown" {
		result.MissingDatabases = a.missingRuleTestDatabases(result.UncertainTypes)
	}
	writeJSON(w, http.StatusOK, result)
}

// simulateCustomRules conservatively evaluates a single input against saved
// enabled custom rules. Missing connection fields and Geo/DNS evaluation are
// unknown, not a negative match. It never evaluates the original YAML chain.
func simulateCustomRules(rules []CustomRule, query, kind string) (RuleTestResult, error) {
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 2048 {
		return RuleTestResult{}, errors.New("请输入 1 至 2048 个字符的域名、IP 或进程名")
	}
	if kind != "destination" && kind != "process" {
		return RuleTestResult{}, errors.New("测试类型必须是目标地址或进程")
	}
	result := RuleTestResult{Query: query, Kind: kind, Status: "no-match"}
	var host string
	var addr netip.Addr
	var processName, processPath string
	if kind == "destination" {
		if parsed, err := netip.ParseAddr(query); err == nil {
			// Normalize IPv4-mapped addresses as IPv4; zones do not affect CIDR membership.
			addr = parsed.Unmap().WithZone("")
		} else {
			var err error
			host, err = idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(query), "."))
			if err != nil || !domainPattern.MatchString(host) || strings.Contains(host, "..") || len(host) > 253 {
				return RuleTestResult{}, errors.New("请输入不含路径或端口的有效域名或 IP 地址")
			}
			for _, label := range strings.Split(host, ".") {
				if len(label) > 63 {
					return RuleTestResult{}, errors.New("域名标签不能超过 63 个字符")
				}
			}
		}
	} else {
		if strings.ContainsAny(query, "\\/") {
			if !filepath.IsAbs(query) || strings.ContainsAny(query, "\r\n,\x00") {
				return RuleTestResult{}, errors.New("请填写有效的完整进程路径，或不含路径的进程名")
			}
			processPath = filepath.Clean(query)
			processName = filepath.Base(processPath)
		} else {
			if !processNamePattern.MatchString(query) {
				return RuleTestResult{}, errors.New("进程名格式无效")
			}
			processName = query
		}
	}

	uncertain := make(map[string]bool)
	for i := range rules {
		rule := &rules[i]
		if !rule.Enabled {
			continue
		}
		matched := false
		switch rule.Type {
		case "PROCESS-NAME":
			if kind == "process" {
				matched = strings.EqualFold(processName, rule.Value)
			} else {
				uncertain[rule.Type] = true
			}
		case "PROCESS-PATH":
			if kind == "process" && processPath != "" {
				matched = strings.EqualFold(processPath, filepath.Clean(rule.Value))
			} else {
				uncertain[rule.Type] = true
			}
		case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD":
			if kind == "process" {
				uncertain[rule.Type] = true
			} else if host != "" {
				switch rule.Type {
				case "DOMAIN":
					matched = host == rule.Value
				case "DOMAIN-SUFFIX":
					matched = host == rule.Value || strings.HasSuffix(host, "."+rule.Value)
				case "DOMAIN-KEYWORD":
					matched = strings.Contains(host, strings.ToLower(rule.Value))
				}
			}
		case "IP-CIDR", "IP-CIDR6":
			if kind == "process" || (host != "" && !rule.NoResolve) {
				uncertain[rule.Type] = true
			} else if addr.IsValid() {
				prefix, err := netip.ParsePrefix(rule.Value)
				if err != nil {
					uncertain[rule.Type] = true
				} else {
					matched = prefix.Contains(addr)
				}
			}
		case "GEOSITE":
			if kind == "process" || host != "" {
				uncertain[rule.Type] = true
			}
		case "GEOIP":
			// The runtime serializer does not emit GEOIP no-resolve, even if stored JSON has that flag.
			if kind == "process" || addr.IsValid() || host != "" {
				uncertain[rule.Type] = true
			}
		default:
			uncertain[rule.Type] = true
		}
		if matched {
			if len(uncertain) == 0 {
				result.Status = "matched"
				result.Matched = rule
			} else {
				result.PossibleMatch = rule
			}
			break
		}
	}
	if len(uncertain) > 0 {
		result.Status = "unknown"
		for ruleType := range uncertain {
			result.UncertainTypes = append(result.UncertainTypes, ruleType)
		}
		sort.Strings(result.UncertainTypes)
	}
	return result, nil
}

// File presence is only a hint; this simulator does not verify or read Geo data.
func (a *Application) missingRuleTestDatabases(types []string) []string {
	checks := map[string][]string{
		"GEOIP":   {"geoip.metadb", "geoip.dat", "Country.mmdb"},
		"GEOSITE": {"GeoSite.dat", "geosite.db"},
	}
	var missing []string
	for _, ruleType := range types {
		files := checks[ruleType]
		if len(files) == 0 {
			continue
		}
		found := false
		for _, name := range files {
			info, err := os.Stat(filepath.Join(a.dataDir, name))
			if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, ruleType)
		}
	}
	sort.Strings(missing)
	return missing
}

func (a *Application) customRulesPath() string { return filepath.Join(a.dataDir, "custom-rules.json") }

func (a *Application) loadCustomRules() ([]CustomRule, error) {
	content, err := os.ReadFile(a.customRulesPath())
	if os.IsNotExist(err) {
		return []CustomRule{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取自定义规则失败: %w", err)
	}
	var rules []CustomRule
	if err := json.Unmarshal(content, &rules); err != nil {
		return nil, fmt.Errorf("custom-rules.json 格式无效: %w", err)
	}
	for i := range rules {
		if err := validateCustomRule(&rules[i]); err != nil {
			return nil, fmt.Errorf("第 %d 条自定义规则无效: %w", i+1, err)
		}
	}
	return rules, nil
}

func (a *Application) saveCustomRules(ctx context.Context, rules []CustomRule) error {
	if len(rules) > 1000 {
		return errors.New("自定义规则最多 1000 条")
	}
	seen := make(map[string]bool)
	for i := range rules {
		if err := validateCustomRule(&rules[i]); err != nil {
			return fmt.Errorf("第 %d 条: %w", i+1, err)
		}
		key := rules[i].Type + "\x00" + rules[i].Value + "\x00" + rules[i].Policy
		if seen[key] {
			return fmt.Errorf("第 %d 条与前面的规则重复", i+1)
		}
		seen[key] = true
	}
	// Rules are global; validate references against every profile after its own
	// overrides, even while disconnected. Never borrow the active profile layer.
	profiles, err := a.loadProfileStore()
	if err != nil {
		return errors.New("无法读取配置列表，未保存规则")
	}
	for _, profile := range profiles.Profiles {
		source, err := os.ReadFile(a.profileContentPath(profile.ID))
		if err != nil {
			return errors.New("无法读取配置，未保存规则")
		}
		if _, _, err := a.prepareProfileConfig(source, profile.ID, a.previewRuntime(), rules, nil); err != nil {
			return fmt.Errorf("规则与某个配置的最终覆写不兼容，未保存规则: %w", err)
		}
	}
	a.mu.Lock()
	runtime, running := a.runtime, a.core != nil
	a.mu.Unlock()
	if running {
		policies, err := a.fetchPolicies(ctx, runtime)
		if err != nil {
			return fmt.Errorf("无法获取当前策略组: %w", err)
		}
		allowed := make(map[string]bool)
		for _, policy := range policies {
			allowed[policy.Name] = true
		}
		for _, rule := range rules {
			if rule.Enabled && !allowed[rule.Policy] {
				return fmt.Errorf("策略“%s”不存在或未加载，请检查规则集状态", rule.Policy)
			}
		}
	}
	content, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if running {
		if err := a.applyRules(ctx, runtime, rules); err != nil {
			return err
		}
	}
	if err := atomicWrite(a.customRulesPath(), content, 0o600); err != nil {
		return fmt.Errorf("保存自定义规则失败: %w", err)
	}
	a.logger.Info("自定义规则已保存", "count", countEnabledRules(rules))
	return nil
}

func validateCustomRule(rule *CustomRule) error {
	rule.Type = strings.ToUpper(strings.TrimSpace(rule.Type))
	rule.Value = strings.TrimSpace(rule.Value)
	rule.Policy = strings.TrimSpace(rule.Policy)
	rule.Note = strings.TrimSpace(rule.Note)
	if rule.ID == "" {
		return errors.New("规则缺少 ID")
	}
	if len(rule.ID) > 100 || utf8.RuneCountInString(rule.Note) > 200 {
		return errors.New("ID 或备注过长")
	}
	if rule.Value == "" {
		return errors.New("请填写匹配内容")
	}
	if rule.Policy == "" || rule.Policy == "GLOBAL" {
		return errors.New("请选择有效策略，不能使用 GLOBAL")
	}
	if strings.ContainsAny(rule.Policy, "\r\n,") || utf8.RuneCountInString(rule.Policy) > 200 {
		return errors.New("策略名称无效")
	}
	if utf8.RuneCountInString(rule.Value) > 2048 {
		return errors.New("匹配内容过长")
	}
	switch rule.Type {
	case "DOMAIN", "DOMAIN-SUFFIX":
		if strings.Contains(rule.Value, "://") || strings.ContainsAny(rule.Value, "/,; \t\r\n") {
			return errors.New("只填写域名，不要包含 http://、路径、空格或逗号")
		}
		ascii, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(rule.Value), "."))
		if err != nil || !domainPattern.MatchString(ascii) {
			return errors.New("域名格式无效，例如 example.com")
		}
		rule.Value = ascii
	case "DOMAIN-KEYWORD":
		if strings.ContainsAny(rule.Value, "\r\n,") {
			return errors.New("关键词不能包含换行或逗号")
		}
	case "IP-CIDR", "IP-CIDR6":
		prefix, err := netip.ParsePrefix(rule.Value)
		if err != nil {
			return errors.New("IP 段格式无效，例如 192.168.0.0/16 或 2001:db8::/32")
		}
		is6 := prefix.Addr().Is6()
		if (rule.Type == "IP-CIDR" && is6) || (rule.Type == "IP-CIDR6" && !is6) {
			return errors.New("IPv4 与 IPv6 规则类型不匹配")
		}
		rule.Value = prefix.Masked().String()
	case "GEOIP":
		if strings.ContainsAny(rule.Value, "\r\n, ") {
			return errors.New("GEOIP 填写国家/地区代码，例如 CN、US、JP，或 PRIVATE")
		}
		rule.Value = strings.ToUpper(rule.Value)
	case "GEOSITE":
		if strings.ContainsAny(rule.Value, "\r\n,") || strings.ContainsAny(rule.Value, " \t") {
			return errors.New("GEOSITE 填写分类名，例如 cn、geolocation-cn、category-ads-all")
		}
		rule.Value = strings.ToLower(rule.Value)
	case "PROCESS-NAME":
		if !processNamePattern.MatchString(rule.Value) {
			return errors.New("进程名格式无效，例如 chrome.exe")
		}
	case "PROCESS-PATH":
		if !filepath.IsAbs(rule.Value) || strings.ContainsAny(rule.Value, "\r\n,") {
			return errors.New("请填写完整的进程路径，例如 C:\\Program Files\\App\\app.exe")
		}
	default:
		return errors.New("暂不支持此匹配类型")
	}
	return nil
}

func mergeCustomRules(root *yaml.Node, custom []CustomRule) error {
	if len(custom) == 0 {
		return nil
	}
	if err := validateCustomRulePolicies(root, custom); err != nil {
		return err
	}
	original := mappingValue(root, "rules")
	var originalRules *yaml.Node
	if original != nil {
		if original.Kind != yaml.SequenceNode {
			return errors.New("原配置中的 rules 必须是列表")
		}
		originalRules = original
	}
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, rule := range custom {
		if !rule.Enabled {
			continue
		}
		value := rule.Type + "," + rule.Value + "," + rule.Policy
		if rule.NoResolve && (rule.Type == "IP-CIDR" || rule.Type == "IP-CIDR6") {
			value += ",no-resolve"
		}
		sequence.Content = append(sequence.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	}
	if originalRules != nil {
		sequence.Content = append(sequence.Content, originalRules.Content...)
	}
	setMapping(root, "rules", sequence)
	return nil
}

func countEnabledRules(rules []CustomRule) int {
	count := 0
	for _, rule := range rules {
		if rule.Enabled {
			count++
		}
	}
	return count
}

func policyNames(policies []PolicyInfo) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(policies))
	for _, policy := range policies {
		if policy.Name == "" || policy.Name == "GLOBAL" || seen[policy.Name] {
			continue
		}
		seen[policy.Name] = true
		result = append(result, policy.Name)
	}
	sort.Strings(result)
	return result
}

func (a *Application) fetchPolicies(ctx context.Context, runtime RuntimeConfig) ([]PolicyInfo, error) {
	var response struct {
		Proxies map[string]struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"proxies"`
	}
	if err := a.localAPI(ctx, runtime, http.MethodGet, "/proxies", nil, &response); err != nil {
		return nil, err
	}
	policies := []PolicyInfo{{Name: "DIRECT", Type: "Direct"}, {Name: "REJECT", Type: "Reject"}}
	for _, proxy := range response.Proxies {
		if proxy.Name == "GLOBAL" {
			continue
		}
		switch proxy.Type {
		case "Selector", "URLTest", "Fallback", "LoadBalance", "Relay", "Direct", "Reject", "Pass":
			policies = append(policies, PolicyInfo{Name: proxy.Name, Type: proxy.Type})
		}
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	return policies, nil
}

func (a *Application) applyRules(ctx context.Context, runtime RuntimeConfig, rules []CustomRule) error {
	source, err := os.ReadFile(filepath.Join(a.dataDir, "source.yaml"))
	if err != nil {
		return errors.New("找不到原始 YAML 配置，请先导入")
	}
	_, rendered, err := a.prepareConfigWithRules(source, runtime, rules)
	if err != nil {
		return err
	}
	if err := a.testConfig(rendered); err != nil {
		return fmt.Errorf("合并后的配置校验失败，旧配置未改变: %w", err)
	}
	runtimePath := filepath.Join(a.dataDir, "runtime.yaml")
	if err := atomicWrite(runtimePath, rendered, 0o600); err != nil {
		return err
	}
	if err := a.localAPI(ctx, runtime, http.MethodPut, "/configs?force=true", map[string]string{"path": runtimePath}, nil); err != nil {
		return fmt.Errorf("热重载失败，规则未保存；请稍后重试或重新连接: %w", err)
	}
	a.logger.Info("自定义规则已热重载", "count", countEnabledRules(rules))
	return nil
}

func (a *Application) localAPI(ctx context.Context, runtime RuntimeConfig, method, path string, payload, result any) error {
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		content, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(content)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(runtime.APIPort)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+runtime.Secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := localHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("mihomo API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

func (a *Application) enabledRuleCount() int {
	rules, err := a.loadCustomRules()
	if err != nil {
		return 0
	}
	return countEnabledRules(rules)
}
