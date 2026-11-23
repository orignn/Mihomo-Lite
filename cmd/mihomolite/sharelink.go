package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type realityOpts struct {
	PublicKey string `yaml:"public-key"`
	ShortID   string `yaml:"short-id"`
}
type wsOpts struct {
	Path                string            `yaml:"path,omitempty"`
	Headers             map[string]string `yaml:"headers,omitempty"`
	MaxEarlyData        int               `yaml:"max-early-data,omitempty"`
	EarlyDataHeaderName string            `yaml:"early-data-header-name,omitempty"`
}
type grpcOpts struct {
	GrpcServiceName string `yaml:"grpc-service-name,omitempty"`
}
type shareProxy struct {
	Name                 string         `yaml:"name"`
	Type                 string         `yaml:"type"`
	Server               string         `yaml:"server"`
	Port                 int            `yaml:"port"`
	UUID                 string         `yaml:"uuid,omitempty"`
	Password             string         `yaml:"password,omitempty"`
	Cipher               string         `yaml:"cipher,omitempty"`
	AlterID              *int           `yaml:"alterId,omitempty"`
	UDP                  bool           `yaml:"udp"`
	Network              string         `yaml:"network,omitempty"`
	TLS                  bool           `yaml:"tls,omitempty"`
	Servername           string         `yaml:"servername,omitempty"`
	SNI                  string         `yaml:"sni,omitempty"`
	ALPN                 []string       `yaml:"alpn,omitempty"`
	ClientFingerprint    string         `yaml:"client-fingerprint,omitempty"`
	Flow                 string         `yaml:"flow,omitempty"`
	RealityOpts          *realityOpts   `yaml:"reality-opts,omitempty"`
	WSOpts               *wsOpts        `yaml:"ws-opts,omitempty"`
	GrpcOpts             *grpcOpts      `yaml:"grpc-opts,omitempty"`
	SkipCertVerify       bool           `yaml:"skip-cert-verify,omitempty"`
	Plugin               string         `yaml:"plugin,omitempty"`
	PluginOpts           map[string]any `yaml:"plugin-opts,omitempty"`
	Obfs                 string         `yaml:"obfs,omitempty"`
	ObfsPassword         string         `yaml:"obfs-password,omitempty"`
	Up                   string         `yaml:"up,omitempty"`
	Down                 string         `yaml:"down,omitempty"`
	Ports                string         `yaml:"ports,omitempty"`
	HopInterval          int            `yaml:"hop-interval,omitempty"`
	CongestionController string         `yaml:"congestion-controller,omitempty"`
	UDPRelayMode         string         `yaml:"udp-relay-mode,omitempty"`
	DisableSNI           bool           `yaml:"disable-sni,omitempty"`
	ReduceRTT            bool           `yaml:"reduce-rtt,omitempty"`
}
type shareGroup struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	Proxies []string `yaml:"proxies"`
}
type shareProfile struct {
	Proxies     []shareProxy `yaml:"proxies"`
	ProxyGroups []shareGroup `yaml:"proxy-groups"`
	Rules       []string     `yaml:"rules"`
}

// Conversion is atomic: any invalid/unsupported node prevents importing a
// misleading partial subscription. Diagnostics never include input values,
// node names, credentials or parser errors which may embed the original URL.
func convertShareLinkSubscription(content []byte, scheme string) (result []byte, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = &shareLinkConversionError{message: resultErr.Error()}
		}
	}()
	links, err := decodeShareLinks(content)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, errors.New("分享链接为空")
	}
	proxies := make([]shareProxy, 0, len(links))
	names := make([]string, 0, len(links))
	used := map[string]bool{}
	for _, name := range []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "PASS-RULE", "COMPATIBLE", "GLOBAL", "节点选择", unifiedProxyName} {
		used[name] = true
	}
	var failures []string
	failed := 0
	for i, link := range links {
		proxy, parseErr := parseShareLink(link)
		if parseErr != nil {
			failed++
			if len(failures) < 8 {
				failures = append(failures, fmt.Sprintf("第 %d 条：%s", i+1, parseErr.Error()))
			}
			continue
		}
		base := proxy.Name
		for n := 2; used[proxy.Name]; n++ {
			proxy.Name = base + " (" + strconv.Itoa(n) + ")"
		}
		used[proxy.Name] = true
		proxies = append(proxies, proxy)
		names = append(names, proxy.Name)
	}
	if failed != 0 {
		return nil, fmt.Errorf("分享链接转换失败：共 %d 条，%d 条无法转换；未导入任何节点，原配置不变。%s", len(links), failed, strings.Join(failures, "；"))
	}
	sort.Strings(names)
	output, err := yaml.Marshal(shareProfile{Proxies: proxies, ProxyGroups: []shareGroup{{Name: "节点选择", Type: "select", Proxies: append(names, "DIRECT")}}, Rules: []string{"MATCH,节点选择"}})
	if err != nil {
		return nil, errors.New("分享链接配置生成失败")
	}
	return append([]byte("# 由 Mihomo Lite 从分享链接转换生成\n"), output...), nil
}

// mergeShareLinkWithTemplate takes the proxies generated from share links and
// grafts them into an existing complete profile, keeping the template's
// rules, DNS, rule-providers and proxy-groups. Group entries that referenced
// removed nodes are dropped; empty groups fall back to all new nodes + DIRECT.
func mergeShareLinkWithTemplate(base, template []byte) ([]byte, error) {
	var baseDoc yaml.Node
	if err := yaml.Unmarshal(base, &baseDoc); err != nil {
		return nil, err
	}
	baseRoot := baseDoc.Content[0]
	var baseProxies *yaml.Node
	for i := 0; i+1 < len(baseRoot.Content); i += 2 {
		if baseRoot.Content[i].Value == "proxies" {
			baseProxies = baseRoot.Content[i+1]
			break
		}
	}
	if baseProxies == nil || len(baseProxies.Content) == 0 {
		return base, nil
	}
	nodeNames := map[string]bool{}
	for _, item := range baseProxies.Content {
		if name := mappingValue(item, "name"); name != nil {
			nodeNames[name.Value] = true
		}
	}
	var tmplDoc yaml.Node
	if err := yaml.Unmarshal(template, &tmplDoc); err != nil {
		return nil, fmt.Errorf("规则模板解析失败: %w", err)
	}
	if len(tmplDoc.Content) != 1 || tmplDoc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("规则模板必须是 YAML 映射")
	}
	root := tmplDoc.Content[0]
	if err := stripUnifiedProxyGeneration(root); err != nil {
		return nil, err
	}
	var groups *yaml.Node
	content := make([]*yaml.Node, 0, len(root.Content)+2)
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if key == "proxies" {
			continue
		}
		content = append(content, root.Content[i], root.Content[i+1])
		if key == "proxy-groups" {
			groups = root.Content[i+1]
		}
	}
	content = append([]*yaml.Node{yamlScalar("proxies"), baseProxies}, content...)
	root.Content = content
	if groups != nil {
		groupNames := map[string]bool{unifiedProxyName: true}
		for _, group := range groups.Content {
			if name := mappingValue(group, "name"); name != nil {
				groupNames[name.Value] = true
			}
		}
		specials := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true, "GLOBAL": true}
		for _, group := range groups.Content {
			proxiesSeq := mappingValue(group, "proxies")
			if proxiesSeq == nil {
				continue
			}
			keep := make([]*yaml.Node, 0, len(proxiesSeq.Content))
			for _, item := range proxiesSeq.Content {
				v := item.Value
				if nodeNames[v] || groupNames[v] || specials[v] {
					keep = append(keep, item)
				}
			}
			if len(keep) == 0 {
				for name := range nodeNames {
					keep = append(keep, yamlScalar(name))
				}
				keep = append(keep, yamlScalar("DIRECT"))
			}
			proxiesSeq.Content = keep
		}
	}
	out, err := yaml.Marshal(&tmplDoc)
	if err != nil {
		return nil, err
	}
	header := "# 由 Mihomo Lite 从分享链接订阅 + 规则模板合并生成\n"
	return append([]byte(header), out...), nil
}

func yamlScalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
