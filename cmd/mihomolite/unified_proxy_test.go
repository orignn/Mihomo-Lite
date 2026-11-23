package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These fixtures are entirely synthetic. None of these tests invokes an
// application, a core, a downloader, the filesystem, or the system proxy.
const unifiedProxyTestNodes = `proxies:
  - name: SYN-A
    type: ss
    server: a.example.invalid
    port: 443
    cipher: aes-128-gcm
    password: SYNTHETIC-ONLY-SS-PASSWORD
    udp: true
  - name: SYN-B
    type: vless
    server: b.example.invalid
    port: 443
    uuid: 11111111-1111-4111-8111-111111111111
    tls: true
    servername: b.example.invalid
`

const unifiedProxyTestProviders = `proxy-providers:
  SYN-Z:
    type: file
    path: ./synthetic-z.yaml
  SYN-P:
    type: file
    path: ./synthetic-p.yaml
`

const unifiedProxyTestRules = `rules:
  - DOMAIN-SUFFIX,synthetic.invalid,SYN-A
  - AND,((NETWORK,TCP),(DOMAIN,other.invalid)),SYN-B
  - MATCH,DIRECT
`

type unifiedProxyTestGroup struct {
	Name          string   `yaml:"name"`
	Type          string   `yaml:"type"`
	Proxies       []string `yaml:"proxies"`
	Use           []string `yaml:"use"`
	EmptyFallback string   `yaml:"empty-fallback"`
}

type unifiedProxyTestProjection struct {
	Groups []unifiedProxyTestGroup `yaml:"proxy-groups"`
	Owner  *struct {
		Schema      int    `yaml:"schema"`
		Name        string `yaml:"name"`
		Fingerprint string `yaml:"fingerprint"`
	} `yaml:"x-mihomolite-unified-proxy"`
}

func unifiedProxyTestRoot(t *testing.T, source string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(source), &doc); err != nil || len(doc.Content) != 1 {
		t.Fatal("invalid synthetic unified-proxy fixture")
	}
	return doc.Content[0]
}

func unifiedProxyTestBytes(t *testing.T, node *yaml.Node) []byte {
	t.Helper()
	data, err := yaml.Marshal(node)
	if err != nil {
		t.Fatal("could not encode synthetic unified-proxy fixture")
	}
	return data
}

func unifiedProxyTestMap(t *testing.T, node *yaml.Node) map[string]any {
	t.Helper()
	var result map[string]any
	if err := node.Decode(&result); err != nil {
		t.Fatal("could not project synthetic unified-proxy fixture")
	}
	return result
}

func unifiedProxyTestProjectionOf(t *testing.T, root *yaml.Node) unifiedProxyTestProjection {
	t.Helper()
	var result unifiedProxyTestProjection
	if err := root.Decode(&result); err != nil {
		t.Fatal("generated unified-proxy structure was not readable")
	}
	return result
}

func unifiedProxyTestAlias(t *testing.T, root *yaml.Node) unifiedProxyTestGroup {
	t.Helper()
	view := unifiedProxyTestProjectionOf(t, root)
	var alias unifiedProxyTestGroup
	count := 0
	for _, group := range view.Groups {
		if group.Name == unifiedProxyName {
			alias = group
			count++
		}
	}
	if count != 1 {
		t.Fatalf("generated unified-proxy group count = %d, want exactly one", count)
	}
	if alias.Type != "select" || alias.EmptyFallback != "REJECT" {
		t.Fatal("generated selector did not retain an explicit fail-closed empty fallback")
	}
	if view.Owner == nil || view.Owner.Schema != 1 || view.Owner.Name != unifiedProxyName {
		t.Fatal("generated selector has no valid ownership marker")
	}
	if len(view.Owner.Fingerprint) != 64 {
		t.Fatal("generated selector has no bounded ownership fingerprint")
	}
	if _, err := hex.DecodeString(view.Owner.Fingerprint); err != nil {
		t.Fatal("generated ownership fingerprint is not hexadecimal")
	}
	return alias
}

func unifiedProxyTestGenerate(t *testing.T, source string) *yaml.Node {
	t.Helper()
	input := []byte(source)
	before := append([]byte(nil), input...)
	var doc yaml.Node
	if err := yaml.Unmarshal(input, &doc); err != nil || len(doc.Content) != 1 {
		t.Fatal("invalid synthetic unified-proxy fixture")
	}
	root := doc.Content[0]
	if err := ensureUnifiedProxy(root); err != nil {
		t.Fatalf("synthetic unified-proxy generation failed: %v", err)
	}
	if !bytes.Equal(before, input) {
		t.Fatal("runtime generation changed the original input bytes")
	}
	unifiedProxyTestAlias(t, root)
	return root
}

func unifiedProxyTestWantStrings(t *testing.T, field string, got, want []string) {
	t.Helper()
	if len(got) != len(want) || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("synthetic alias %s = %v, want %v", field, got, want)
	}
}

func unifiedProxyTestGroupNode(t *testing.T, root *yaml.Node, name string) *yaml.Node {
	t.Helper()
	groups := mappingValue(root, "proxy-groups")
	if groups == nil || groups.Kind != yaml.SequenceNode {
		t.Fatal("generated group list was not materialized")
	}
	for _, group := range groups.Content {
		value := mappingValue(group, "name")
		if value != nil && value.Value == name {
			return group
		}
	}
	t.Fatal("synthetic group was not found")
	return nil
}

func TestUnifiedProxyPreferredNodeSelectionNames(t *testing.T) {
	cases := []struct {
		name, groups, target string
	}{
		{"decorated-node-selection", `proxy-groups:
  - {name: "🚀 节点选择", type: select, proxies: [SYN-A, SYN-B, DIRECT]}
  - {name: 流媒体业务, type: select, proxies: [SYN-A]}
`, "🚀 节点选择"},
		{"decorated-proxy-selection", `proxy-groups:
  - {name: "🔰 代理选择", type: select, proxies: [SYN-A, 自动测速, DIRECT]}
  - {name: 自动测速, type: url-test, proxies: [SYN-B], url: "https://probe.example.invalid/", interval: 300}
  - {name: 流媒体业务, type: select, proxies: [SYN-A]}
`, "🔰 代理选择"},
		{"proxy-name", `proxy-groups:
  - {name: PROXY, type: select, proxies: [SYN-A, SYN-B]}
  - {name: 下载业务, type: select, proxies: [SYN-B]}
`, "PROXY"},
		{"node-selection-nested-real-outbound", `proxy-groups:
  - {name: 节点选择, type: select, proxies: [自动测速, DIRECT]}
  - {name: 自动测速, type: fallback, proxies: [SYN-A, SYN-B], interval: 300}
`, "节点选择"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestGenerate(t, unifiedProxyTestNodes+tc.groups+unifiedProxyTestRules)
			alias := unifiedProxyTestAlias(t, root)
			unifiedProxyTestWantStrings(t, "proxies", alias.Proxies, []string{tc.target})
			unifiedProxyTestWantStrings(t, "use", alias.Use, nil)
		})
	}
}

func TestUnifiedProxyUnknownOrAmbiguousSelectionFallback(t *testing.T) {
	cases := []struct {
		name, nodes, groups string
		want                []string
	}{
		{"unique-all-node-brand", unifiedProxyTestNodes, `proxy-groups:
  - {name: 合成机场入口, type: select, proxies: [SYN-A, SYN-B]}
  - {name: 流媒体业务, type: select, proxies: [SYN-A]}
`, []string{"合成机场入口"}},
		{"unique-include-all-base", unifiedProxyTestNodes, `proxy-groups:
  - {name: 合成品牌节点, type: select, include-all-proxies: true}
  - {name: 视频业务, type: select, proxies: [SYN-A]}
`, []string{"合成品牌节点"}},
		{"nested-business-does-not-replace-base", unifiedProxyTestNodes, `proxy-groups:
  - {name: 合成基础节点, type: select, proxies: [SYN-A, SYN-B]}
  - {name: 所有业务出口, type: select, proxies: [合成基础节点]}
`, []string{"合成基础节点"}},
		{"two-full-brand-groups-are-ambiguous", unifiedProxyTestNodes, `proxy-groups:
  - {name: 第一业务, type: select, proxies: [SYN-A, SYN-B]}
  - {name: 第二业务, type: select, proxies: [SYN-B, SYN-A]}
`, []string{"SYN-A", "SYN-B"}},
		{"multiple-canonical-selectors-are-ambiguous", unifiedProxyTestNodes, `proxy-groups:
  - {name: 节点选择, type: select, proxies: [SYN-A]}
  - {name: PROXY, type: select, proxies: [SYN-B]}
`, []string{"SYN-A", "SYN-B"}},
		{"partial-business-group", unifiedProxyTestNodes, `proxy-groups:
  - {name: 视频业务, type: select, proxies: [SYN-A]}
`, []string{"SYN-A", "SYN-B"}},
		{"business-suffix-is-not-a-canonical-name", unifiedProxyTestNodes, `proxy-groups:
  - {name: "视频 PROXY", type: select, proxies: [SYN-A]}
`, []string{"SYN-A", "SYN-B"}},
		{"canonical-name-but-non-selector", unifiedProxyTestNodes, `proxy-groups:
  - {name: 节点选择, type: url-test, proxies: [SYN-A, SYN-B]}
`, []string{"SYN-A", "SYN-B"}},
		{"canonical-direct-only-group", unifiedProxyTestNodes, `proxy-groups:
  - {name: 节点选择, type: select, proxies: [DIRECT, REJECT]}
`, []string{"SYN-A", "SYN-B"}},
		{"unknown-full-group-with-direct", unifiedProxyTestNodes, `proxy-groups:
  - {name: 合成业务, type: select, proxies: [DIRECT, SYN-A, SYN-B]}
`, []string{"SYN-A", "SYN-B"}},
		{"unknown-full-group-with-named-direct-node", unifiedProxyTestNodes + "  - {name: LOCAL, type: direct}\n", `proxy-groups:
  - {name: 合成业务, type: select, proxies: [LOCAL, SYN-A, SYN-B]}
`, []string{"SYN-A", "SYN-B"}},
		{"unknown-include-all-with-named-direct-node", unifiedProxyTestNodes + "  - {name: LOCAL, type: direct}\n", `proxy-groups:
  - {name: 合成业务, type: select, include-all-proxies: true}
`, []string{"SYN-A", "SYN-B"}},
		{"unknown-full-group-with-filter", unifiedProxyTestNodes, `proxy-groups:
  - {name: 地区业务, type: select, proxies: [SYN-A, SYN-B], filter: SYN-A}
`, []string{"SYN-A", "SYN-B"}},
		{"unknown-full-group-with-exclude-filter", unifiedProxyTestNodes, `proxy-groups:
  - {name: 地区业务, type: select, proxies: [SYN-A, SYN-B], exclude-filter: SYN-B}
`, []string{"SYN-A", "SYN-B"}},
		{"unknown-full-group-with-exclude-type", unifiedProxyTestNodes, `proxy-groups:
  - {name: 协议业务, type: select, proxies: [SYN-A, SYN-B], exclude-type: vless}
`, []string{"SYN-A", "SYN-B"}},
		{"unknown-business-indirect-direct", unifiedProxyTestNodes, `proxy-groups:
  - {name: 上层业务, type: select, proxies: [业务测速]}
  - {name: 业务测速, type: url-test, proxies: [DIRECT, SYN-A, SYN-B]}
`, []string{"SYN-A", "SYN-B"}},
		{"unknown-business-indirect-filter", unifiedProxyTestNodes, `proxy-groups:
  - {name: 上层业务, type: select, proxies: [地区测速]}
  - {name: 地区测速, type: url-test, proxies: [SYN-A, SYN-B], filter: SYN-A}
`, []string{"SYN-A", "SYN-B"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestGenerate(t, tc.nodes+tc.groups+unifiedProxyTestRules)
			alias := unifiedProxyTestAlias(t, root)
			unifiedProxyTestWantStrings(t, "proxies", alias.Proxies, tc.want)
			unifiedProxyTestWantStrings(t, "use", alias.Use, nil)
		})
	}
}

func TestUnifiedProxyRawNodesProvidersAndFailClosedEmptySources(t *testing.T) {
	cases := []struct {
		name, source string
		proxies, use []string
	}{
		{"ungrouped-two-nodes", unifiedProxyTestNodes, []string{"SYN-A", "SYN-B"}, nil},
		{"single-ss-node", `proxies:
  - {name: ONLY, type: ss, server: single.example.invalid, port: 443, cipher: aes-128-gcm, password: SYNTHETIC-ONLY}
`, []string{"ONLY"}, nil},
		{"single-vless-node", `proxies:
  - {name: ONLY, type: vless, server: single.example.invalid, port: 443, uuid: "11111111-1111-4111-8111-111111111111"}
`, []string{"ONLY"}, nil},
		{"provider-only-no-direct-default", unifiedProxyTestProviders, nil, []string{"SYN-P", "SYN-Z"}},
		{"mixed-nodes-and-providers", unifiedProxyTestNodes + unifiedProxyTestProviders, []string{"SYN-A", "SYN-B"}, []string{"SYN-P", "SYN-Z"}},
		{"empty-node-and-provider-lists", "proxies: []\nproxy-providers: {}\n", []string{"REJECT"}, nil},
		{"no-declared-sources", "mode: rule\nrules: [\"MATCH,DIRECT\"]\n", []string{"REJECT"}, nil},
		{"only-non-proxy-outbounds", `proxies:
  - {name: LOCAL, type: direct}
  - {name: BLOCK, type: reject}
  - {name: DROP, type: reject-drop}
  - {name: NEXT, type: pass}
  - {name: COMPAT, type: compatible}
  - {name: RESOLVER, type: dns}
proxy-groups:
  - {name: 节点选择, type: select, proxies: [LOCAL, DIRECT]}
`, []string{"REJECT"}, nil},
		{"exclude-non-proxy-nodes-from-mixed-pool", unifiedProxyTestNodes + `  - {name: LOCAL, type: direct}
  - {name: BLOCK, type: reject}
  - {name: NEXT, type: pass}
  - {name: COMPAT, type: compatible}
  - {name: RESOLVER, type: dns}
`, []string{"SYN-A", "SYN-B"}, nil},
		{"provider-name-is-not-an-outbound-policy", `proxy-providers:
  SYN-ONLY: {type: file, path: ./synthetic-only.yaml}
`, nil, []string{"SYN-ONLY"}},
		{"provider-and-node-have-separate-namespaces", `proxies:
  - {name: SYN-P, type: ss, server: same-name.example.invalid, port: 443, cipher: aes-128-gcm, password: SYNTHETIC-ONLY}
proxy-providers:
  SYN-P: {type: file, path: ./synthetic-only.yaml}
`, []string{"SYN-P"}, []string{"SYN-P"}},
		{"preferred-provider-backed-selector", unifiedProxyTestProviders + `proxy-groups:
  - {name: 节点选择, type: select, use: [SYN-P, SYN-Z]}
`, []string{"节点选择"}, nil},
		{"unknown-provider-base-selector", unifiedProxyTestProviders + `proxy-groups:
  - {name: 合成品牌入口, type: select, use: [SYN-P, SYN-Z]}
`, []string{"合成品牌入口"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestGenerate(t, tc.source)
			alias := unifiedProxyTestAlias(t, root)
			unifiedProxyTestWantStrings(t, "proxies", alias.Proxies, tc.proxies)
			unifiedProxyTestWantStrings(t, "use", alias.Use, tc.use)
		})
	}
}

func TestUnifiedProxyPreservesSourceFieldsAndExplicitGroupOptions(t *testing.T) {
	source := unifiedProxyTestNodes + unifiedProxyTestProviders + unifiedProxyTestRules + `mode: rule
log-level: info
dns:
  enable: true
  nameserver: ["https://dns.example.invalid/dns-query"]
rule-providers:
  synthetic-rules: {type: file, behavior: domain, path: ./synthetic-rules.txt}
proxy-groups:
  - name: PROXY
    type: select
    proxies: [SYN-A, SYN-B]
    use: [SYN-P, SYN-Z]
    url: https://probe.example.invalid/
    interval: 321
    lazy: false
    hidden: true
    icon: synthetic-icon
    filter: SYN
    exclude-filter: OMIT
    exclude-type: direct
    include-all-providers: false
    disable-udp: true
    test-timeout: 4500
    max-failed-times: 4
    empty-fallback: DIRECT
  - name: 业务分组
    type: select
    proxies: [SYN-A]
    custom-option: {nested: [one, two]}
`
	before := unifiedProxyTestMap(t, unifiedProxyTestRoot(t, source))
	root := unifiedProxyTestGenerate(t, source)
	after := unifiedProxyTestMap(t, root)
	for key, value := range before {
		if key != "proxy-groups" && !reflect.DeepEqual(value, after[key]) {
			t.Fatalf("runtime generation changed the synthetic source field %q", key)
		}
	}
	var original, generated struct {
		Groups []map[string]any `yaml:"proxy-groups"`
	}
	if err := unifiedProxyTestRoot(t, source).Decode(&original); err != nil {
		t.Fatal("synthetic original group projection failed")
	}
	if err := root.Decode(&generated); err != nil || len(generated.Groups) != len(original.Groups)+1 {
		t.Fatal("runtime generation changed original group count or ordering")
	}
	for i, group := range original.Groups {
		if _, exists := group["empty-fallback"]; !exists {
			group["empty-fallback"] = "REJECT"
		}
		if !reflect.DeepEqual(group, generated.Groups[i]) {
			t.Fatalf("runtime generation changed original group options at synthetic row %d", i+1)
		}
	}
}

func TestUnifiedProxyAliasAndMergeEffectiveValues(t *testing.T) {
	cases := []struct {
		name, source, target string
	}{
		{"group-list-and-scalar-alias", unifiedProxyTestNodes + `label: &label "🚀 节点选择"
list: &groups [{name: *label, type: select, proxies: [SYN-A, SYN-B]}]
proxy-groups: *groups
`, "🚀 节点选择"},
		{"group-item-merge-with-explicit-name", unifiedProxyTestNodes + `base-group: &base {name: OLD, type: select, proxies: [SYN-A, SYN-B], hidden: true}
proxy-groups: [{<<: *base, name: PROXY}]
`, "PROXY"},
		{"root-inherits-node-and-group-lists", `defaults: &base
  proxies:
    - {name: ONLY, type: ss, server: alias.example.invalid, port: 443, cipher: aes-128-gcm, password: SYNTHETIC-ONLY}
  proxy-groups: [{name: 节点选择, type: select, proxies: [ONLY]}]
<<: *base
`, "节点选择"},
		{"explicit-root-group-override", unifiedProxyTestNodes + `defaults: &base {proxy-groups: [{name: OLD, type: select, proxies: [SYN-A]}]}
<<: *base
proxy-groups: [{name: PROXY, type: select, proxies: [SYN-A, SYN-B]}]
`, "PROXY"},
		{"merge-sequence-first-wins", unifiedProxyTestNodes + `one: &one {proxy-groups: [{name: 节点选择, type: select, proxies: [SYN-A]}]}
two: &two {proxy-groups: [{name: PROXY, type: select, proxies: [SYN-B]}]}
<<: [*one, *two]
`, "节点选择"},
		{"merge-sequence-reversed", unifiedProxyTestNodes + `one: &one {proxy-groups: [{name: 节点选择, type: select, proxies: [SYN-A]}]}
two: &two {proxy-groups: [{name: PROXY, type: select, proxies: [SYN-B]}]}
<<: [*two, *one]
`, "PROXY"},
		{"provider-and-use-list-alias", `sources: &providers {SYN-P: {type: file, path: ./synthetic-p.yaml}}
uses: &uses [SYN-P]
proxy-providers: *providers
proxy-groups: [{name: PROXY, type: select, use: *uses}]
`, "PROXY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestGenerate(t, tc.source)
			unifiedProxyTestWantStrings(t, "proxies", unifiedProxyTestAlias(t, root).Proxies, []string{tc.target})
			if groups := mappingValue(root, "proxy-groups"); groups == nil || groups.Kind != yaml.SequenceNode {
				t.Fatal("effective inherited or aliased groups were not exposed as a runtime list")
			}
		})
	}
}

func TestMaterializeConfigRootExposesInheritedSafetyFieldsWithoutAnchorPollution(t *testing.T) {
	source := `defaults: &defaults
  mixed-port: &shared-port 1443
  port: 7890
  socks-port: 7891
  allow-lan: true
  bind-address: 0.0.0.0
  external-controller: 0.0.0.0:9090
  secret: &shared-secret SYNTHETIC-ONLY-ANCHORED-PASSWORD
  listeners: &shared-listeners
    - {name: synthetic-listener, type: socks, port: 1443}
  rules: &shared-rules ["DOMAIN,synthetic.invalid,SYN-A", "MATCH,DIRECT"]
<<: *defaults
other-listeners: *shared-listeners
proxies:
  - name: SYN-A
    type: ss
    server: a.example.invalid
    port: *shared-port
    cipher: aes-128-gcm
    password: *shared-secret
proxy-groups: [{name: PROXY, type: select, proxies: [SYN-A]}]
`
	root := unifiedProxyTestRoot(t, source)
	before := unifiedProxyTestBytes(t, root)
	resolved, err := materializeConfigRoot(root)
	if err != nil || resolved == nil || resolved.Kind != yaml.MappingNode {
		t.Fatal("synthetic inherited configuration was not materialized")
	}
	for _, key := range []string{"mixed-port", "port", "socks-port", "allow-lan", "bind-address", "external-controller", "secret", "listeners", "rules"} {
		if mappingValue(resolved, key) == nil {
			t.Fatalf("inherited safety field %q remained invisible to runtime overrides", key)
		}
	}
	listeners := mappingValue(resolved, "listeners")
	if listeners.Kind != yaml.SequenceNode || len(listeners.Content) != 1 {
		t.Fatal("inherited nonempty listeners were silently lost during materialization")
	}
	setScalar(resolved, "secret", "SYNTHETIC-RUNTIME-ONLY")
	setScalar(resolved, "mixed-port", "17891", "!!int")
	setScalar(listeners.Content[0], "port", "18891", "!!int")
	rules := mappingValue(resolved, "rules")
	if rules.Kind != yaml.SequenceNode || len(rules.Content) != 2 {
		t.Fatal("inherited rule alias was not materialized as a list")
	}
	*rules.Content[0] = *yamlScalar("DOMAIN,changed.invalid,REJECT")
	var got struct {
		Proxies []struct {
			Port     int    `yaml:"port"`
			Password string `yaml:"password"`
		} `yaml:"proxies"`
		OtherListeners []struct {
			Port int `yaml:"port"`
		} `yaml:"other-listeners"`
		Defaults struct {
			Secret string   `yaml:"secret"`
			Rules  []string `yaml:"rules"`
		} `yaml:"defaults"`
	}
	if err := resolved.Decode(&got); err != nil || len(got.Proxies) != 1 || len(got.OtherListeners) != 1 {
		t.Fatal("materialized synthetic credentials or aliases were not readable")
	}
	if got.Proxies[0].Port != 1443 || got.Proxies[0].Password != "SYNTHETIC-ONLY-ANCHORED-PASSWORD" {
		t.Fatal("runtime port or secret override contaminated an anchored node credential")
	}
	if got.OtherListeners[0].Port != 1443 || got.Defaults.Secret != "SYNTHETIC-ONLY-ANCHORED-PASSWORD" || len(got.Defaults.Rules) != 2 || got.Defaults.Rules[0] != "DOMAIN,synthetic.invalid,SYN-A" {
		t.Fatal("runtime mutation contaminated another reference to a shared YAML anchor")
	}
	if !bytes.Equal(before, unifiedProxyTestBytes(t, root)) {
		t.Fatal("materialization or runtime overrides mutated the original parsed source tree")
	}
}

func TestMaterializeConfigRootExplicitSafetyOverridesWin(t *testing.T) {
	root := unifiedProxyTestRoot(t, `one: &one {port: 7890, secret: FIRST, listeners: [{name: synthetic, type: socks, port: 10001}], rules: ["MATCH,DIRECT"]}
two: &two {port: 7891, secret: SECOND}
<<: [*one, *two]
port: 20001
secret: EXPLICIT
listeners: []
rules: ["MATCH,REJECT"]
`)
	resolved, err := materializeConfigRoot(root)
	if err != nil {
		t.Fatal("valid explicit synthetic overrides were rejected")
	}
	var got struct {
		Port      int         `yaml:"port"`
		Secret    string      `yaml:"secret"`
		Listeners []yaml.Node `yaml:"listeners"`
		Rules     []string    `yaml:"rules"`
	}
	if err := resolved.Decode(&got); err != nil || got.Port != 20001 || got.Secret != "EXPLICIT" || len(got.Listeners) != 0 {
		t.Fatal("explicit runtime safety overrides lost to inherited defaults")
	}
	unifiedProxyTestWantStrings(t, "effective source rules", got.Rules, []string{"MATCH,REJECT"})
}

func TestUnifiedProxyRejectsOwnershipConflictsAndInvalidStructures(t *testing.T) {
	cases := []struct{ name, source string }{
		{"reserved-node", `proxies:
  - {name: 统一代理, type: ss, server: synthetic.invalid, port: 443, cipher: aes-128-gcm, password: SYNTHETIC-ONLY}
`},
		{"reserved-provider", unifiedProxyTestNodes + "proxy-providers: {统一代理: {type: file, path: ./synthetic-reserved.yaml}}\n"},
		{"unowned-reserved-group", unifiedProxyTestNodes + "proxy-groups: [{name: 统一代理, type: select, proxies: [SYN-A]}]\n"},
		{"unowned-reserved-marker", unifiedProxyTestNodes + "x-mihomolite-unified-proxy: null\n"},
		{"marker-without-owned-group", unifiedProxyTestNodes + "x-mihomolite-unified-proxy: {schema: 1, name: 统一代理, fingerprint: SYNTHETIC-FINGERPRINT}\n"},
		{"duplicate-nodes", `proxies:
  - {name: SAME, type: ss, server: a.invalid, port: 443, cipher: aes-128-gcm, password: SYNTHETIC-ONLY}
  - {name: SAME, type: vless, server: b.invalid, port: 443, uuid: "11111111-1111-4111-8111-111111111111"}
`},
		{"duplicate-groups", unifiedProxyTestNodes + "proxy-groups: [{name: SAME, type: select, proxies: [SYN-A]}, {name: SAME, type: select, proxies: [SYN-B]}]\n"},
		{"node-group-namespace-collision", unifiedProxyTestNodes + "proxy-groups: [{name: SYN-A, type: select, proxies: [SYN-B]}]\n"},
		{"empty-node-name", "proxies: [{name: '', type: ss}]\n"},
		{"empty-group-name", unifiedProxyTestNodes + "proxy-groups: [{name: '', type: select, proxies: [SYN-A]}]\n"},
		{"duplicate-provider-map-keys", unifiedProxyTestNodes + "proxy-providers: {SAME: {type: file, path: ./synthetic-a.yaml}, SAME: {type: file, path: ./synthetic-b.yaml}}\n"},
		{"invalid-node-list", "proxies: PRIVATE_STRUCTURE_SENTINEL\n"},
		{"invalid-group-list", unifiedProxyTestNodes + "proxy-groups: PRIVATE_STRUCTURE_SENTINEL\n"},
		{"invalid-use-list", unifiedProxyTestNodes + unifiedProxyTestProviders + "proxy-groups: [{name: PROXY, type: select, use: PRIVATE_STRUCTURE_SENTINEL}]\n"},
		{"duplicate-root-key", "proxies: []\nproxies: PRIVATE_STRUCTURE_SENTINEL\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestRoot(t, tc.source)
			before := unifiedProxyTestBytes(t, root)
			err := ensureUnifiedProxy(root)
			if err == nil {
				t.Fatal("conflicting or malformed synthetic configuration was accepted")
			}
			if strings.Contains(err.Error(), "PRIVATE_STRUCTURE_SENTINEL") {
				t.Fatal("structural diagnostic leaked an arbitrary synthetic source scalar")
			}
			if !bytes.Equal(before, unifiedProxyTestBytes(t, root)) {
				t.Fatal("rejected generation mutated the input source tree")
			}
		})
	}
}

func TestUnifiedProxyRejectsGroupDialerAndProviderCycles(t *testing.T) {
	cases := []struct{ name, source string }{
		{"group-self-cycle", unifiedProxyTestNodes + "proxy-groups: [{name: LOOP, type: select, proxies: [LOOP]}]\n"},
		{"mutual-group-cycle", unifiedProxyTestNodes + "proxy-groups: [{name: ONE, type: select, proxies: [TWO]}, {name: TWO, type: select, proxies: [ONE]}]\n"},
		{"node-self-dialer", strings.Replace(unifiedProxyTestNodes, "    udp: true\n", "    udp: true\n    dialer-proxy: SYN-A\n", 1)},
		{"mutual-node-dialers", strings.Replace(strings.Replace(unifiedProxyTestNodes, "    udp: true\n", "    udp: true\n    dialer-proxy: SYN-B\n", 1), "    tls: true\n", "    tls: true\n    dialer-proxy: SYN-A\n", 1)},
		{"node-dials-pending-alias", strings.Replace(unifiedProxyTestNodes, "    udp: true\n", "    udp: true\n    dialer-proxy: 统一代理\n", 1)},
		{"node-group-alias-backedge", strings.Replace(unifiedProxyTestNodes, "    udp: true\n", "    udp: true\n    dialer-proxy: 业务分组\n", 1) + "proxy-groups: [{name: 业务分组, type: select, proxies: [统一代理]}]\n"},
		{"provider-dials-pending-alias", "proxy-providers: {SYN-P: {type: file, path: ./synthetic-p.yaml, proxy: 统一代理}}\n"},
		{"provider-group-cycle", "proxy-providers: {SYN-P: {type: file, path: ./synthetic-p.yaml, proxy: PROXY}}\nproxy-groups: [{name: PROXY, type: select, use: [SYN-P]}]\n"},
		{"provider-include-all-cycle", "proxy-providers: {SYN-P: {type: file, path: ./synthetic-p.yaml, proxy: PROXY}}\nproxy-groups: [{name: PROXY, type: select, include-all-providers: true}]\n"},
		{"unknown-group-member", unifiedProxyTestNodes + "proxy-groups: [{name: PROXY, type: select, proxies: [PRIVATE_REFERENCE_SENTINEL]}]\n"},
		{"unknown-dialer", strings.Replace(unifiedProxyTestNodes, "    udp: true\n", "    udp: true\n    dialer-proxy: PRIVATE_REFERENCE_SENTINEL\n", 1)},
		{"unknown-provider-reference", unifiedProxyTestNodes + "proxy-groups: [{name: PROXY, type: select, use: [PRIVATE_REFERENCE_SENTINEL]}]\n"},
		{"unknown-provider-download-proxy", "proxy-providers: {SYN-P: {type: file, path: ./synthetic-p.yaml, proxy: PRIVATE_REFERENCE_SENTINEL}}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestRoot(t, tc.source)
			before := unifiedProxyTestBytes(t, root)
			err := ensureUnifiedProxy(root)
			if err == nil {
				t.Fatal("synthetic cyclic or missing outbound dependency was accepted")
			}
			if strings.Contains(err.Error(), "PRIVATE_REFERENCE_SENTINEL") {
				t.Fatal("dependency diagnostic echoed an arbitrary synthetic outbound name")
			}
			if !bytes.Equal(before, unifiedProxyTestBytes(t, root)) {
				t.Fatal("dependency rejection mutated the source tree")
			}
		})
	}
}

func TestUnifiedProxyAllowsAcyclicBusinessReferencesAndDownloadDialers(t *testing.T) {
	cases := []struct{ name, source string }{
		{"business-refers-to-unified-alias", unifiedProxyTestNodes + "proxy-groups: [{name: 业务分组, type: select, proxies: [统一代理, DIRECT]}]\n"},
		{"node-dials-another-real-node", strings.Replace(unifiedProxyTestNodes, "    udp: true\n", "    udp: true\n    dialer-proxy: SYN-B\n", 1)},
		{"provider-download-dials-static-node", unifiedProxyTestNodes + "proxy-providers: {SYN-P: {type: file, path: ./synthetic-p.yaml, proxy: SYN-A}}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestGenerate(t, tc.source)
			unifiedProxyTestWantStrings(t, "proxies", unifiedProxyTestAlias(t, root).Proxies, []string{"SYN-A", "SYN-B"})
		})
	}
}

func TestUnifiedProxyOwnedGroupIsIdempotent(t *testing.T) {
	cases := []struct{ name, source string }{
		{"preferred-group", unifiedProxyTestNodes + "proxy-groups: [{name: PROXY, type: select, proxies: [SYN-A, SYN-B]}]\n"},
		{"raw-nodes", unifiedProxyTestNodes},
		{"provider-only", unifiedProxyTestProviders},
		{"empty-fail-closed", "proxies: []\nproxy-providers: {}\n"},
		{"business-refers-to-alias", unifiedProxyTestNodes + "proxy-groups: [{name: 业务分组, type: select, proxies: [统一代理, DIRECT]}]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestGenerate(t, tc.source)
			before := unifiedProxyTestBytes(t, root)
			if err := ensureUnifiedProxy(root); err != nil {
				t.Fatal("second helper call rejected its own generated group")
			}
			unifiedProxyTestAlias(t, root)
			if !bytes.Equal(before, unifiedProxyTestBytes(t, root)) {
				t.Fatal("second helper call changed the generated group, marker, or original options")
			}
			reparsed := unifiedProxyTestRoot(t, string(before))
			if err := ensureUnifiedProxy(reparsed); err != nil {
				t.Fatal("serialized ownership marker did not survive reparsing")
			}
			unifiedProxyTestAlias(t, reparsed)
			if !bytes.Equal(before, unifiedProxyTestBytes(t, reparsed)) {
				t.Fatal("repreparing serialized output duplicated or changed the owned generation layer")
			}
		})
	}
}

func TestUnifiedProxyRejectsTamperedOwnedGroupAndMarker(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *yaml.Node)
	}{
		{"group-type", func(t *testing.T, root *yaml.Node) {
			setScalar(unifiedProxyTestGroupNode(t, root, unifiedProxyName), "type", "url-test")
		}},
		{"group-members", func(t *testing.T, root *yaml.Node) {
			group := unifiedProxyTestGroupNode(t, root, unifiedProxyName)
			setMapping(group, "proxies", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{yamlScalar("DIRECT")}})
		}},
		{"empty-fallback", func(t *testing.T, root *yaml.Node) {
			setScalar(unifiedProxyTestGroupNode(t, root, unifiedProxyName), "empty-fallback", "DIRECT")
		}},
		{"extra-owned-option", func(t *testing.T, root *yaml.Node) {
			setScalar(unifiedProxyTestGroupNode(t, root, unifiedProxyName), "hidden", "true", "!!bool")
		}},
		{"marker-schema", func(t *testing.T, root *yaml.Node) {
			setScalar(mappingValue(root, unifiedProxyMarker), "schema", "2", "!!int")
		}},
		{"marker-name", func(t *testing.T, root *yaml.Node) {
			setScalar(mappingValue(root, unifiedProxyMarker), "name", "PRIVATE_OWNER_SENTINEL")
		}},
		{"marker-fingerprint", func(t *testing.T, root *yaml.Node) {
			setScalar(mappingValue(root, unifiedProxyMarker), "fingerprint", strings.Repeat("0", 64))
		}},
		{"removed-marker", func(t *testing.T, root *yaml.Node) { deleteMapping(root, unifiedProxyMarker) }},
		{"removed-owned-group", func(t *testing.T, root *yaml.Node) {
			setMapping(root, "proxy-groups", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
		}},
		{"duplicate-owned-group", func(t *testing.T, root *yaml.Node) {
			group := unifiedProxyTestGroupNode(t, root, unifiedProxyName)
			duplicate := unifiedProxyTestRoot(t, string(unifiedProxyTestBytes(t, group)))
			groups := mappingValue(root, "proxy-groups")
			groups.Content = append(groups.Content, duplicate)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestGenerate(t, unifiedProxyTestNodes)
			tc.mutate(t, root)
			before := unifiedProxyTestBytes(t, root)
			err := ensureUnifiedProxy(root)
			if err == nil {
				t.Fatal("tampered protected selector or ownership marker was accepted")
			}
			if strings.Contains(err.Error(), "PRIVATE_OWNER_SENTINEL") {
				t.Fatal("ownership diagnostic exposed an arbitrary marker name")
			}
			if !bytes.Equal(before, unifiedProxyTestBytes(t, root)) {
				t.Fatal("ownership rejection silently repaired or replaced the protected group")
			}
		})
	}
}

func TestMaterializeConfigRootRejectsInvalidAndCyclicYAML(t *testing.T) {
	cases := []struct {
		name string
		root *yaml.Node
	}{
		{"nil", nil},
		{"scalar", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "PRIVATE_MATERIALIZE_SENTINEL"}},
		{"sequence", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}},
		{"duplicate-map-key", unifiedProxyTestRoot(t, "port: 7890\nport: PRIVATE_MATERIALIZE_SENTINEL\n")},
		{"recursive-alias", unifiedProxyTestRoot(t, "loop: &loop {self: *loop}\n")},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%02d-%s", i, tc.name), func(t *testing.T) {
			resolved, err := materializeConfigRoot(tc.root)
			if err == nil || resolved != nil {
				t.Fatal("invalid or recursive synthetic configuration was materialized")
			}
			if strings.Contains(err.Error(), "PRIVATE_MATERIALIZE_SENTINEL") {
				t.Fatal("materialization diagnostic leaked an arbitrary source scalar")
			}
		})
	}
}
