package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const maxOverrideDNSServers = 16
const maxOverrideDNSAddressBytes = 2048
const maxOverrideDNSListBytes = 16 << 10

// Values describe this independent layer, never the subscription/global result.
// An unrepresentable value is read-only, not an invitation to overwrite it.
type overrideSimple struct {
	LogLevel    string   `json:"logLevel"`
	DNSServers  []string `json:"dnsServers"`
	LogEditable bool     `json:"logEditable"`
	DNSEditable bool     `json:"dnsEditable"`
	HasAdvanced bool     `json:"hasAdvanced"`
}

type overrideFormRequest struct {
	YAML       string
	LogLevel   *string
	DNSServers *[]string
}

type overrideFormResponse struct {
	YAML   string         `json:"yaml"`
	Simple overrideSimple `json:"simple"`
}

func validOverrideLogLevel(value string) bool {
	switch value {
	case "debug", "info", "warning", "error", "silent":
		return true
	}
	return false
}

// Deliberately a local syntax allowlist, not a DNS lookup or kernel validation.
// Advanced transports/options remain available through the existing YAML layer.
func validOverrideDNSAddress(value string) bool {
	if value == "" || len(value) > maxOverrideDNSAddressBytes || !utf8.ValidString(value) {
		return false
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return false
	}
	for _, r := range decoded {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '\\' {
			return false
		}
	}
	if value == "system" {
		return true
	}
	if !strings.Contains(value, "://") {
		if net.ParseIP(value) != nil {
			return true
		}
		host, port, err := net.SplitHostPort(value)
		return err == nil && net.ParseIP(host) != nil && validOverrideDNSPort(port)
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" || strings.Contains(value, "#") {
		return false
	}
	if strings.HasSuffix(u.Host, ":") || (u.Port() != "" && !validOverrideDNSPort(u.Port())) {
		return false
	}
	host := u.Hostname()
	if strings.HasPrefix(u.Host, "[") && net.ParseIP(host) == nil {
		return false
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(u.Host, "[") {
		return false // URL IPv6 literals must be bracketed.
	}
	switch u.Scheme {
	case "https":
		return validOverrideDNSHost(host)
	case "tls":
		return validOverrideDNSHost(host) && u.Path == "" && u.RawQuery == "" && !u.ForceQuery
	case "udp", "tcp":
		return net.ParseIP(host) != nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery
	}
	return false
}

func validOverrideDNSPort(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535
}

func validOverrideDNSHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 || strings.Trim(host, "0123456789.") == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func validOverrideDNSList(values []string) bool {
	if len(values) > maxOverrideDNSServers {
		return false
	}
	total := 0
	for _, value := range values {
		total += len(value)
		if !validOverrideDNSAddress(value) || total > maxOverrideDNSListBytes {
			return false
		}
	}
	return true
}

func decodeOverrideFormRequest(data []byte) (overrideFormRequest, error) {
	var req overrideFormRequest
	if !utf8.Valid(data) {
		return req, errors.New("请求必须为 UTF-8 JSON")
	}
	fields, err := decodeStrictObject(data, map[string]bool{"yaml": true, "logLevel": true, "dnsServers": true})
	if err != nil {
		return req, errors.New("请求必须是 JSON 对象，且不能有未知、重复字段或额外内容")
	}
	text, exists := fields["yaml"]
	if !exists || len(text) == 0 || text[0] != '"' || json.Unmarshal(text, &req.YAML) != nil {
		return req, errors.New("yaml 必须提供字符串，不能为 null")
	}
	if raw, exists := fields["logLevel"]; exists {
		var value string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil || (value != "" && !validOverrideLogLevel(value)) {
			return req, errors.New("日志级别须为继承、debug、info、warning、error 或 silent")
		}
		req.LogLevel = &value
	}
	if raw, exists := fields["dnsServers"]; exists {
		var items []json.RawMessage
		if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil || len(items) > maxOverrideDNSServers {
			return req, errors.New("dnsServers 必须是最多 16 项的字符串数组，不能为 null")
		}
		values := make([]string, 0, len(items))
		for _, item := range items {
			var value string
			if len(item) == 0 || item[0] != '"' || json.Unmarshal(item, &value) != nil {
				return req, errors.New("DNS 地址必须是字符串，不能为 null")
			}
			values = append(values, value)
		}
		if !validOverrideDNSList(values) {
			return req, errors.New("DNS 地址无效或超过限额；支持 IP/端口、HTTPS DoH、TLS、UDP/TCP IP 和 system，不接受空白或换行")
		}
		req.DNSServers = &values
	}
	return req, nil
}

// Reuse every strict YAML/security-marker check, then keep the original syntax
// tree: materializing the edited tree would silently flatten unrelated aliases.
func parseOverrideFormYAML(text string) (*yaml.Node, *yaml.Node, error) {
	resolved, err := decodeOverrideYAML(text)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(text) == "" {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, resolved, nil
	}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text), &doc) != nil || len(doc.Content) != 1 {
		return nil, nil, errors.New("无法解析覆写表单")
	}
	return doc.Content[0], resolved, nil
}

func overrideFormComplex(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" || n.Tag == "!!merge" {
		return true
	}
	for _, child := range n.Content {
		if overrideFormComplex(child) {
			return true
		}
	}
	return false
}

// A merge that supplies the target prevents safe removal (it would reappear).
// Unrelated merge/alias fields do not prevent editing another ordinary field.
func overrideFormMergeProvides(root *yaml.Node, key string) bool {
	merged := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Tag == "!!merge" {
			merged.Content = append(merged.Content, root.Content[i], root.Content[i+1])
		}
	}
	if len(merged.Content) == 0 {
		return false
	}
	resolved, err := materializeConfigRoot(merged)
	return err != nil || mappingValue(resolved, key) != nil
}

func overrideFormOrdinaryField(root *yaml.Node, key string) bool {
	if root.Kind != yaml.MappingNode || root.Tag != "!!map" || root.Anchor != "" || overrideFormMergeProvides(root, key) {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key && overrideFormComplex(root.Content[i]) {
			return false
		}
	}
	return !overrideFormComplex(mappingValue(root, key))
}

func overrideSimpleFromNodes(root, resolved *yaml.Node) overrideSimple {
	result := overrideSimple{DNSServers: []string{}, LogEditable: true, DNSEditable: true}
	log := mappingValue(resolved, "log-level")
	if log != nil {
		result.LogLevel = log.Value
		result.LogEditable = log.Kind == yaml.ScalarNode && log.Tag == "!!str" && validOverrideLogLevel(log.Value)
	}
	result.LogEditable = result.LogEditable && overrideFormOrdinaryField(root, "log-level")
	if rawLog := mappingValue(root, "log-level"); rawLog != nil && rawLog.Tag != "!!str" {
		result.LogEditable = false
	}
	dns := mappingValue(resolved, "dns")
	rawDNS := mappingValue(root, "dns")
	// DNS may contain unrelated anchors/merge under fallback etc.; only its
	// container, key and nameserver subtree need to be ordinary and unshared.
	result.DNSEditable = root.Anchor == "" && root.Tag == "!!map" && !overrideFormMergeProvides(root, "dns")
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "dns" && overrideFormComplex(root.Content[i]) {
			result.DNSEditable = false
		}
	}
	if dns != nil {
		if dns.Kind != yaml.MappingNode || rawDNS == nil || rawDNS.Kind != yaml.MappingNode || rawDNS.Tag != "!!map" || rawDNS.Anchor != "" {
			result.DNSEditable = false
		} else {
			result.DNSEditable = result.DNSEditable && overrideFormOrdinaryField(rawDNS, "nameserver")
		}
		if dns.Kind == yaml.MappingNode {
			if ns := mappingValue(dns, "nameserver"); ns != nil {
				if ns.Kind != yaml.SequenceNode || len(ns.Content) == 0 {
					result.DNSEditable = false // Explicit [] replaces, unlike form inheritance.
				} else {
					for _, item := range ns.Content {
						if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
							result.DNSEditable = false
						} else {
							result.DNSServers = append(result.DNSServers, item.Value)
						}
					}
					result.DNSEditable = result.DNSEditable && validOverrideDNSList(result.DNSServers)
				}
				if rawDNS != nil {
					if rawNS := mappingValue(rawDNS, "nameserver"); rawNS != nil {
						if rawNS.Kind != yaml.SequenceNode || rawNS.Tag != "!!seq" {
							result.DNSEditable = false
						}
						for _, item := range rawNS.Content {
							if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
								result.DNSEditable = false
							}
						}
					}
				}
			}
		}
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, root.Content[i+1]
		if key == "log-level" {
			continue
		}
		if key != "dns" || value.Kind != yaml.MappingNode {
			result.HasAdvanced = true
			continue
		}
		for j := 0; j+1 < len(value.Content); j += 2 {
			if value.Content[j].Value != "nameserver" {
				result.HasAdvanced = true
			}
		}
	}
	result.HasAdvanced = result.HasAdvanced || !result.LogEditable || !result.DNSEditable
	return result
}

func readOverrideSimple(text string) (overrideSimple, error) {
	root, resolved, err := parseOverrideFormYAML(text)
	if err != nil {
		return overrideSimple{}, err
	}
	return overrideSimpleFromNodes(root, resolved), nil
}

// Replace in place to preserve key order and comments; no shared node is edited.
func setOverrideFormNode(root *yaml.Node, key string, value *yaml.Node) {
	if previous := mappingValue(root, key); previous != nil {
		value.HeadComment, value.LineComment, value.FootComment = previous.HeadComment, previous.LineComment, previous.FootComment
		*previous = *value
		return
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func convertOverrideForm(req overrideFormRequest) (overrideFormResponse, error) {
	root, resolved, err := parseOverrideFormYAML(req.YAML)
	if err != nil {
		return overrideFormResponse{}, err
	}
	simple := overrideSimpleFromNodes(root, resolved)
	if req.LogLevel != nil && (!simple.LogEditable || (*req.LogLevel != "" && !validOverrideLogLevel(*req.LogLevel))) {
		return overrideFormResponse{}, errors.New("当前日志字段无法安全通过表单修改，请使用高级 YAML")
	}
	if req.DNSServers != nil && (!simple.DNSEditable || !validOverrideDNSList(*req.DNSServers)) {
		return overrideFormResponse{}, errors.New("当前 DNS 字段无法安全通过表单修改，或 DNS 地址无效，请使用高级 YAML")
	}
	changed := false
	if req.LogLevel != nil && *req.LogLevel != simple.LogLevel {
		changed = true
		if *req.LogLevel == "" {
			deleteMapping(root, "log-level")
		} else {
			setOverrideFormNode(root, "log-level", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: *req.LogLevel})
		}
	}
	if req.DNSServers != nil {
		values := *req.DNSServers
		equal := len(values) == len(simple.DNSServers)
		for i := range values {
			if !equal || values[i] != simple.DNSServers[i] {
				equal = false
				break
			}
		}
		if !equal {
			changed = true
			dns := mappingValue(root, "dns")
			if len(values) == 0 {
				deleteMapping(dns, "nameserver")
				if len(dns.Content) == 0 {
					deleteMapping(root, "dns")
				}
			} else {
				if dns == nil {
					dns = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
					setOverrideFormNode(root, "dns", dns)
				}
				ns := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
				for _, value := range values {
					ns.Content = append(ns.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
				}
				setOverrideFormNode(dns, "nameserver", ns)
			}
		}
	}
	if !changed {
		return overrideFormResponse{YAML: req.YAML, Simple: simple}, nil
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if enc.Encode(root) != nil || enc.Close() != nil {
		return overrideFormResponse{}, errors.New("无法生成覆写 YAML")
	}
	text := out.String()
	result, err := readOverrideSimple(text) // Check the generated YAML against the same strict boundary.
	if err != nil {
		return overrideFormResponse{}, err
	}
	return overrideFormResponse{YAML: text, Simple: result}, nil
}

// Pure conversion: no profile reads, persistence, operation reservation or core.
func (a *Application) OverrideForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "不支持的请求方法"})
		return
	}
	if r.Header.Get("X-Mihomo-Lite") != "1" {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "请求校验失败，请刷新页面后重试"})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxOverrideYAMLBytes*6))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "请求过大或无法读取"})
		return
	}
	req, err := decodeOverrideFormRequest(data)
	if err == nil {
		var result overrideFormResponse
		result, err = convertOverrideForm(req)
		if err == nil {
			writeJSON(w, http.StatusOK, result)
			return
		}
	}
	writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
}
