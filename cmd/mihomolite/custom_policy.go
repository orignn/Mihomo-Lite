package main

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// This diagnostic is safe to expose: it contains counts/positions, never names.
type customPolicyCompatibilityError struct{ message string }

func (e *customPolicyCompatibilityError) Error() string { return e.message }

// validateCustomRulePolicies checks the candidate, not the currently running core.
// Decode only a name projection: YAML aliases/merges are resolved without
// changing the source tree. Provider source names are not outbound policies.
func validateCustomRulePolicies(root *yaml.Node, custom []CustomRule) error {
	if countEnabledRules(custom) == 0 {
		return nil
	}
	if root == nil || root.Kind != yaml.MappingNode {
		return errors.New("无法读取待应用配置的代理/策略组名称，请检查 YAML 结构")
	}
	type namedPolicy struct {
		Name string `yaml:"name"`
	}
	var names struct {
		Proxies []namedPolicy `yaml:"proxies"`
		Groups  []namedPolicy `yaml:"proxy-groups"`
	}
	if err := root.Decode(&names); err != nil {
		// A YAML decode error can contain untrusted scalar contents.
		return errors.New("无法读取待应用配置的代理/策略组名称，请检查 YAML 结构")
	}
	allowed := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true}
	for _, list := range [][]namedPolicy{names.Proxies, names.Groups} {
		for _, policy := range list {
			if policy.Name != "" {
				allowed[policy.Name] = true
			}
		}
	}
	missingCount := 0
	seen := make(map[string]bool)
	rows := make([]string, 0, 5)
	for index, rule := range custom {
		if !rule.Enabled || allowed[rule.Policy] {
			continue
		}
		missingCount++
		seen[rule.Policy] = true
		if len(rows) < 5 {
			rows = append(rows, fmt.Sprint(index+1))
		}
	}
	if missingCount == 0 {
		return nil
	}
	positions := strings.Join(rows, "、")
	if missingCount > len(rows) {
		positions += " 等"
	}
	// Policy names are user-controlled too and may themselves contain a URL or
	// credentials. Identify saved row positions, never echo arbitrary names.
	return &customPolicyCompatibilityError{message: fmt.Sprintf("有 %d 条启用的自定义规则引用了 %d 个待应用配置中不存在的策略（按已保存顺序第 %s 条，策略名请在规则列表查看）。自定义规则跨配置保留；请先在「自定义规则」停用这些规则并保存，再导入或切换配置，随后按新配置的策略组重新选择目标并启用。未自动删除规则、跳过规则或改为 DIRECT。", missingCount, len(seen), positions)}
}
