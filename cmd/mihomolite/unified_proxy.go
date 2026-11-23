package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/dlclark/regexp2"
	"gopkg.in/yaml.v3"
)

const unifiedProxyName = "统一代理"
const unifiedProxyMarker = "x-mihomolite-unified-proxy"

// Materialize runtime-only effective values before changing any anchored value.
// The original source bytes are never changed. This also keeps inherited local
// listeners/settings subject to the same safety overrides as explicit keys.
func materializeConfigRoot(root *yaml.Node) (*yaml.Node, error) {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, errors.New("配置必须是 YAML 映射")
	}
	var values map[string]any
	if err := root.Decode(&values); err != nil {
		return nil, errors.New("YAML 配置无法展开，请检查重复键、字段类型或循环引用")
	}
	var resolved yaml.Node
	if err := resolved.Encode(values); err != nil {
		return nil, errors.New("YAML 配置无法生成有效映射")
	}
	return &resolved, nil
}

type unifiedNode struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`
	Dialer string `yaml:"dialer-proxy"`
}

type unifiedGroup struct {
	Name             string   `yaml:"name"`
	Type             string   `yaml:"type"`
	Proxies          []string `yaml:"proxies"`
	Use              []string `yaml:"use"`
	IncludeAll       bool     `yaml:"include-all"`
	IncludeNodes     bool     `yaml:"include-all-proxies"`
	IncludeProviders bool     `yaml:"include-all-providers"`
	Filter           string   `yaml:"filter"`
	ExcludeFilter    string   `yaml:"exclude-filter"`
	ExcludeType      string   `yaml:"exclude-type"`
	EmptyFallback    string   `yaml:"empty-fallback"`
}

type unifiedOwner struct {
	Schema      int    `yaml:"schema"`
	Name        string `yaml:"name"`
	Fingerprint string `yaml:"fingerprint"`
}

type unifiedProvider struct {
	Proxy    string        `yaml:"proxy"`
	Dialer   string        `yaml:"dialer-proxy"`
	Payload  []unifiedNode `yaml:"payload"`
	Override struct {
		Dialer      *string  `yaml:"dialer-proxy"`
		Expressions []string `yaml:"override-expr"`
	} `yaml:"override"`
}

func unifiedProviderDialers(provider unifiedProvider) []string {
	if provider.Override.Dialer != nil {
		if *provider.Override.Dialer == "" {
			return nil
		}
		return []string{*provider.Override.Dialer}
	}
	if provider.Dialer != "" {
		return []string{provider.Dialer}
	}
	result := make([]string, 0)
	for _, node := range provider.Payload {
		if node.Dialer != "" {
			result = append(result, node.Dialer)
		}
	}
	return result
}

type unifiedConfig struct {
	Nodes     []unifiedNode              `yaml:"proxies"`
	Groups    []unifiedGroup             `yaml:"proxy-groups"`
	Providers map[string]unifiedProvider `yaml:"proxy-providers"`
	Owner     *unifiedOwner              `yaml:"x-mihomolite-unified-proxy"`
}

var unifiedSpecials = map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "PASS-RULE": true, "COMPATIBLE": true, "GLOBAL": true}

func realUnifiedNode(node unifiedNode) bool {
	switch strings.ToLower(node.Type) {
	case "", "direct", "reject", "reject-drop", "pass", "pass-rule", "compatible", "dns", "rematch":
		return false
	}
	return !unifiedSpecials[node.Name]
}

func unifiedFingerprint(group map[string]any) (string, error) {
	data, err := json.Marshal(group)
	if err != nil {
		return "", errors.New("统一代理所有权内容无法校验")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalUnifiedLabel(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
}

func unifiedAdapterType(kind string) string {
	switch strings.ToLower(kind) {
	case "ss":
		return "Shadowsocks"
	case "ssr":
		return "ShadowsocksR"
	case "select":
		return "Selector"
	case "url-test":
		return "URLTest"
	case "load-balance":
		return "LoadBalance"
	case "reject-drop":
		return "RejectDrop"
	case "pass-rule":
		return "PassRule"
	case "gost-relay":
		return "GostRelay"
	}
	return kind
}

func unifiedPatterns(value string) ([]*regexp2.Regexp, error) {
	if value == "" {
		return nil, nil
	}
	result := make([]*regexp2.Regexp, 0)
	for _, pattern := range strings.Split(value, string(rune(96))) {
		expression, err := regexp2.Compile(pattern, regexp2.None)
		if err != nil {
			return nil, errors.New("原组过滤表达式无效，无法安全生成统一代理")
		}
		expression.MatchTimeout = 50 * time.Millisecond
		result = append(result, expression)
	}
	return result, nil
}

func unifiedMatches(patterns []*regexp2.Regexp, name string) (bool, error) {
	for _, pattern := range patterns {
		matched, err := pattern.MatchString(name)
		if err != nil {
			return false, errors.New("原组过滤计算超时，无法安全生成统一代理")
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func ensureUnifiedProxy(root *yaml.Node) error {
	resolved, err := materializeConfigRoot(root)
	if err != nil {
		return err
	}
	var model unifiedConfig
	var raw struct {
		Groups []map[string]any `yaml:"proxy-groups"`
	}
	if resolved.Decode(&model) != nil || resolved.Decode(&raw) != nil {
		return errors.New("代理节点、分组或 provider 结构无效，无法生成统一代理")
	}
	aliasIndex := -1
	for i, group := range model.Groups {
		if group.Name == unifiedProxyName {
			if aliasIndex >= 0 {
				return errors.New("统一代理组重复，未覆盖原配置")
			}
			aliasIndex = i
		}
	}
	for _, node := range model.Nodes {
		if node.Name == unifiedProxyName {
			return errors.New("配置中的代理节点占用了保留名称「统一代理」，未覆盖原配置")
		}
	}
	if model.Owner != nil {
		if aliasIndex < 0 || model.Owner.Schema != 1 || model.Owner.Name != unifiedProxyName {
			return errors.New("统一代理所有权标记无效，未覆盖原配置")
		}
		fingerprint, err := unifiedFingerprint(raw.Groups[aliasIndex])
		if err != nil || fingerprint != model.Owner.Fingerprint {
			return errors.New("已生成的统一代理组被改动，请使用原订阅或移除生成层后重试")
		}
	} else if aliasIndex >= 0 || mappingValue(resolved, unifiedProxyMarker) != nil {
		return errors.New("配置已占用保留名称或标记「统一代理」，未重命名或覆盖原分组")
	}
	if aliasIndex >= 0 {
		model.Groups = append(model.Groups[:aliasIndex], model.Groups[aliasIndex+1:]...)
		raw.Groups = append(raw.Groups[:aliasIndex], raw.Groups[aliasIndex+1:]...)
	}
	// Node and group names share the outbound namespace. Provider names do not.
	declared := make(map[string]bool)
	pool := make([]string, 0, len(model.Nodes))
	nodeTypes := make(map[string]string)
	groupTypes := make(map[string]string)
	rawGroups := make(map[string]map[string]any)
	for i, group := range model.Groups {
		groupTypes[group.Name] = unifiedAdapterType(group.Type)
		rawGroups[group.Name] = raw.Groups[i]
	}
	resources := make(map[string]bool)
	graph := make(map[string][]string)
	choices := make(map[string][]string)
	for _, node := range model.Nodes {
		if node.Name == "" || strings.IndexByte(node.Name, 0) >= 0 || declared[node.Name] {
			return errors.New("代理节点或分组名称为空/重复，无法生成统一代理")
		}
		declared[node.Name] = true
		nodeTypes[node.Name] = unifiedAdapterType(node.Type)
		graph[node.Name] = nil
		if node.Dialer != "" {
			graph[node.Name] = append(graph[node.Name], node.Dialer)
		}
		if realUnifiedNode(node) {
			pool = append(pool, node.Name)
			resources[node.Name] = true
		}
	}
	providerNames := make([]string, 0, len(model.Providers))
	for name, provider := range model.Providers {
		if len(provider.Override.Expressions) > 0 {
			return errors.New("provider 含动态覆写表达式，无法静态验证统一代理拨号依赖；请改用明确覆写字段")
		}
		if name == unifiedProxyName {
			return errors.New("provider 已占用保留名称「统一代理」，未覆盖原配置")
		}
		if name == "" || strings.IndexByte(name, 0) >= 0 {
			return errors.New("provider 名称无效，无法生成统一代理")
		}
		key := "\x00provider:" + name
		providerNames = append(providerNames, name)
		resources[key] = true
		graph[key] = nil
		if provider.Proxy != "" {
			graph[key] = append(graph[key], provider.Proxy)
		}
		graph[key] = append(graph[key], unifiedProviderDialers(provider)...)
	}
	sort.Strings(providerNames)
	for _, group := range model.Groups {
		if group.Name == "" || strings.IndexByte(group.Name, 0) >= 0 || declared[group.Name] {
			return errors.New("代理节点或分组名称为空/重复，无法生成统一代理")
		}
		declared[group.Name] = true
		filter, err := unifiedPatterns(group.Filter)
		if err != nil {
			return err
		}
		exclude, err := unifiedPatterns(group.ExcludeFilter)
		if err != nil {
			return err
		}
		candidates := append([]string(nil), group.Proxies...)
		if group.IncludeAll || group.IncludeNodes {
			for _, node := range model.Nodes {
				matches := len(filter) == 0
				if len(filter) > 0 {
					matches, err = unifiedMatches(filter, node.Name)
					if err != nil {
						return err
					}
				}
				if matches {
					candidates = append(candidates, node.Name)
				}
			}
		}
		for _, candidate := range candidates {
			excluded, err := unifiedMatches(exclude, candidate)
			if err != nil {
				return err
			}
			if excluded {
				continue
			}
			kind := nodeTypes[candidate]
			if kind == "" {
				kind = groupTypes[candidate]
			}
			if kind == "" && unifiedSpecials[candidate] {
				kind = unifiedAdapterType(candidate)
			}
			excludedType := false
			for _, part := range strings.Split(group.ExcludeType, "|") {
				if part != "" && strings.EqualFold(kind, part) {
					excludedType = true
				}
			}
			if !excludedType {
				choices[group.Name] = append(choices[group.Name], candidate)
			}
		}
		selectedProviders := group.Use
		if group.IncludeAll || group.IncludeProviders {
			selectedProviders = providerNames
		}
		for _, provider := range selectedProviders {
			if _, ok := model.Providers[provider]; !ok {
				return errors.New("原分组引用了不存在的 provider，未生成统一代理")
			}
			choices[group.Name] = append(choices[group.Name], "\x00provider:"+provider)
		}

		graph[group.Name] = append([]string(nil), choices[group.Name]...)
		if group.EmptyFallback != "" {
			graph[group.Name] = append(graph[group.Name], group.EmptyFallback)
		}
	}
	graph[unifiedProxyName] = nil
	// Implicit GLOBAL can select the new alias; never treat it as Direct.
	if _, exists := graph["GLOBAL"]; !exists {
		graph["GLOBAL"] = []string{unifiedProxyName}
		for name := range declared {
			if name != "GLOBAL" {
				graph["GLOBAL"] = append(graph["GLOBAL"], name)
			}
		}
	}
	// Reject unknown references and pre-existing cycles, but allow the pending
	// alias to be referenced by source groups; the final graph catches backedges.
	if err := checkUnifiedGraph(graph); err != nil {
		return err
	}
	coverage := func(start string) (map[string]bool, bool) {
		found := make(map[string]bool)
		seen := make(map[string]bool)
		dependsOnAlias := false
		var walk func(string)
		walk = func(name string) {
			if seen[name] {
				return
			}
			seen[name] = true
			if name == unifiedProxyName {
				dependsOnAlias = true
				return
			}
			if resources[name] {
				found[name] = true
			}
			for _, next := range choices[name] {
				walk(next)
			}
		}
		walk(start)
		return found, dependsOnAlias
	}
	nodesByName := make(map[string]unifiedNode)
	groupsByName := make(map[string]unifiedGroup)
	for _, node := range model.Nodes {
		nodesByName[node.Name] = node
	}
	for _, group := range model.Groups {
		groupsByName[group.Name] = group
	}
	// Structural inference must not mistake a business wrapper or a custom
	// direct adapter for a general proxy selector. Dialer/download routes are
	// transport dependencies, not selectable outbound options.
	safeBase := func(start string) bool {
		seen := make(map[string]bool)
		var safe func(string) bool
		safe = func(name string) bool {
			if seen[name] {
				return true
			}
			seen[name] = true
			if node, exists := nodesByName[name]; exists {
				return realUnifiedNode(node)
			}
			if strings.HasPrefix(name, "\x00provider:") {
				return true
			}
			if unifiedSpecials[name] || name == unifiedProxyName {
				return false
			}
			group, exists := groupsByName[name]
			if !exists || group.Filter != "" || group.ExcludeFilter != "" || group.ExcludeType != "" {
				return false
			}
			if group.EmptyFallback != "" && group.EmptyFallback != "REJECT" && group.EmptyFallback != "REJECT-DROP" {
				return false
			}
			if group.IncludeAll || group.IncludeNodes {
				for _, node := range model.Nodes {
					if !realUnifiedNode(node) {
						return false
					}
				}
			}
			for _, choice := range group.Proxies {
				if !safe(choice) {
					return false
				}
			}
			return true
		}
		return safe(start)
	}
	emptyClosed := func(start string) bool {
		seen := make(map[string]bool)
		var safe func(string) bool
		safe = func(name string) bool {
			if seen[name] {
				return true
			}
			seen[name] = true
			group, exists := rawGroups[name]
			if !exists {
				return true
			}
			if value, exists := group["empty-fallback"]; exists {
				fallback, ok := value.(string)
				if !ok || fallback == "" || fallback == "DIRECT" || fallback == "COMPATIBLE" || fallback == "PASS" || fallback == "PASS-RULE" || fallback == "GLOBAL" {
					return false
				}
				if node, exists := nodesByName[fallback]; exists && !realUnifiedNode(node) {
					return false
				}
			}
			for _, choice := range choices[name] {
				if !safe(choice) {
					return false
				}
			}
			return true
		}
		return safe(start)
	}
	preferred := make([]string, 0)
	full := make([]string, 0)
	hints := map[string]bool{"节点选择": true, "代理选择": true, "代理节点": true, "proxy": true, "proxies": true, "proxyselect": true, "proxyselector": true, "nodeselect": true, "nodeselector": true}
	for _, group := range model.Groups {
		if strings.ToLower(group.Type) != "select" || unifiedSpecials[group.Name] {
			continue
		}
		reachable, aliasDependency := coverage(group.Name)
		if len(reachable) == 0 || aliasDependency || !emptyClosed(group.Name) {
			continue
		}
		if hints[canonicalUnifiedLabel(group.Name)] {
			preferred = append(preferred, group.Name)
		}
		if len(reachable) == len(resources) && group.Filter == "" && group.ExcludeFilter == "" && group.ExcludeType == "" {
			if safeBase(group.Name) {
				full = append(full, group.Name)
			}
		}
	}
	target := ""
	if len(preferred) == 1 {
		target = preferred[0]
	}
	if target == "" && len(preferred) == 0 {
		base := make([]string, 0)
		for _, name := range full {
			referencesOther := false
			for _, next := range choices[name] {
				for _, candidate := range full {
					if next == candidate && candidate != name {
						referencesOther = true
					}
				}
			}
			if !referencesOther {
				base = append(base, name)
			}
		}
		if len(base) == 1 {
			target = base[0]
		}
	}
	alias := map[string]any{"name": unifiedProxyName, "type": "select", "empty-fallback": "REJECT", "exclude-type": "Direct|Reject|RejectDrop|Pass|PassRule|Compatible|Dns|Rematch"}
	if target != "" {
		alias["proxies"] = []string{target}
		graph[unifiedProxyName] = []string{target}
	} else {
		if len(pool) > 0 {
			alias["proxies"] = pool
			graph[unifiedProxyName] = append(graph[unifiedProxyName], pool...)
		}
		if len(providerNames) > 0 {
			alias["use"] = providerNames
			for _, name := range providerNames {
				graph[unifiedProxyName] = append(graph[unifiedProxyName], "\x00provider:"+name)
			}
		}
		if len(pool) == 0 && len(providerNames) == 0 {
			alias["proxies"] = []string{"REJECT"}
		}
	}
	if err := checkUnifiedGraph(graph); err != nil {
		return err
	}
	// Missing kernel defaults used to be COMPATIBLE (Direct). Fill only absent
	// runtime group defaults; do not overwrite explicit upstream settings/options.
	for _, group := range raw.Groups {
		if _, exists := group["empty-fallback"]; !exists {
			group["empty-fallback"] = "REJECT"
		}
	}
	fingerprint, err := unifiedFingerprint(alias)
	if err != nil {
		return err
	}
	groups := append(raw.Groups, alias)
	var groupsNode, markerNode yaml.Node
	if groupsNode.Encode(groups) != nil || markerNode.Encode(map[string]any{"schema": 1, "name": unifiedProxyName, "fingerprint": fingerprint}) != nil {
		return errors.New("统一代理生成失败，原配置未改动")
	}
	setMapping(resolved, "proxy-groups", &groupsNode)
	setMapping(resolved, unifiedProxyMarker, &markerNode)
	*root = *resolved
	return nil
}

// Remove only verified app-owned generation before replacing template nodes.
func stripUnifiedProxyGeneration(root *yaml.Node) error {
	resolved, err := materializeConfigRoot(root)
	if err != nil {
		return err
	}
	if mappingValue(resolved, unifiedProxyMarker) == nil {
		*root = *resolved
		return nil
	}
	var model unifiedConfig
	var raw struct {
		Groups []map[string]any `yaml:"proxy-groups"`
	}
	if resolved.Decode(&model) != nil || resolved.Decode(&raw) != nil || model.Owner == nil || model.Owner.Schema != 1 || model.Owner.Name != unifiedProxyName {
		return errors.New("模板的统一代理所有权标记无效")
	}
	index := -1
	for i, group := range model.Groups {
		if group.Name == unifiedProxyName {
			if index >= 0 {
				return errors.New("模板中的统一代理组重复")
			}
			index = i
		}
	}
	if index < 0 {
		return errors.New("模板的统一代理生成内容缺失")
	}
	fingerprint, err := unifiedFingerprint(raw.Groups[index])
	if err != nil || fingerprint != model.Owner.Fingerprint {
		return errors.New("模板的统一代理生成内容被改动，未覆盖")
	}
	raw.Groups = append(raw.Groups[:index], raw.Groups[index+1:]...)
	var groups yaml.Node
	if err := groups.Encode(raw.Groups); err != nil {
		return errors.New("模板代理组无法展开")
	}
	setMapping(resolved, "proxy-groups", &groups)
	deleteMapping(resolved, unifiedProxyMarker)
	*root = *resolved
	return nil
}

func checkUnifiedGraph(graph map[string][]string) error {
	colors := make(map[string]uint8)
	budget := 1 << 18
	var visit func(string, int) error
	visit = func(name string, depth int) error {
		budget--
		if depth > 256 || budget < 0 {
			return errors.New("代理依赖过深或过多，无法安全生成统一代理")
		}
		if _, exists := graph[name]; !exists {
			if unifiedSpecials[name] {
				return nil
			}
			return errors.New("代理、分组或下载出口引用不存在，无法生成统一代理")
		}
		if colors[name] == 1 {
			return errors.New("代理组、节点拨号或 provider 下载存在循环依赖，无法生成统一代理")
		}
		if colors[name] == 2 {
			return nil
		}
		colors[name] = 1
		for _, next := range graph[name] {
			if err := visit(next, depth+1); err != nil {
				return err
			}
		}
		colors[name] = 2
		return nil
	}
	for name := range graph {
		if err := visit(name, 0); err != nil {
			return err
		}
	}
	return nil
}
