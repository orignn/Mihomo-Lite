package main

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestUnifiedProxyReviewedDependencyEdges(t *testing.T) {
	cases := []struct {
		name, source string
		reject       bool
	}{
		{"provider-node-dialer-cycle", "proxy-providers: {P: {type: file, path: ./fake.yaml, dialer-proxy: 统一代理}}\n", true},
		{"provider-inline-dialer-cycle", "proxy-providers: {P: {type: inline, payload: [{name: A, type: ss, dialer-proxy: 统一代理}]}}\n", true},
		{"provider-override-dialer-cycle", "proxy-providers: {P: {type: file, path: ./fake.yaml, override: {dialer-proxy: 统一代理}}}\n", true},
		{"provider-explicit-empty-override-clears-cycle", "proxy-providers: {P: {type: file, path: ./fake.yaml, dialer-proxy: 统一代理, override: {dialer-proxy: ''}}}\n", false},
		{"provider-nonempty-override-wins", "proxy-providers: {P: {type: inline, payload: [{name: A, type: ss, dialer-proxy: 统一代理}], override: {dialer-proxy: DIRECT}}}\n", false},
		{"provider-top-level-empty-does-not-clear-payload", "proxy-providers: {P: {type: inline, dialer-proxy: '', payload: [{name: A, type: ss, dialer-proxy: 统一代理}]}}\n", true},
		{"empty-fallback-backedge", "proxies: [{name: A, type: ss, dialer-proxy: PROXY}]\nproxy-providers: {P: {type: file, path: ./fake.yaml}}\nproxy-groups: [{name: PROXY, type: select, use: [P], empty-fallback: A}]\n", true},
		{"include-all-overrides-ignored-use", "proxy-providers: {P: {type: file, path: ./fake.yaml}}\nproxy-groups: [{name: PROXY, type: select, include-all-providers: true, use: [IGNORED]}]\n", false},
		{"exclude-filter-removes-false-cycle", "proxies: [{name: A, type: ss, dialer-proxy: G}, {name: B, type: ss}]\nproxy-groups: [{name: G, type: select, proxies: [A, B], exclude-filter: '^A$'}]\n", false},
		{"group-filter-does-not-delete-explicit-choice", "proxies: [{name: A, type: ss, dialer-proxy: G}, {name: B, type: ss}]\nproxy-groups: [{name: G, type: select, proxies: [A, B], filter: '^B$'}]\n", true},
		{"exclude-type-uses-adapter-type", "proxies: [{name: A, type: ss, dialer-proxy: G}, {name: B, type: vless}]\nproxy-groups: [{name: G, type: select, proxies: [A, B], exclude-type: Shadowsocks}]\n", false},
		{"raw-type-is-not-adapter-type", "proxies: [{name: A, type: ss, dialer-proxy: G}, {name: B, type: vless}]\nproxy-groups: [{name: G, type: select, proxies: [A, B], exclude-type: ss}]\n", true},
		{"lookahead-exclude-filter", "proxies: [{name: A, type: ss, dialer-proxy: G}, {name: B, type: vless}]\nproxy-groups: [{name: G, type: select, proxies: [A, B], exclude-filter: '^(?!B).*$'}]\n", false},
		{"implicit-global-dialer-backedge", "proxies: [{name: A, type: ss, dialer-proxy: GLOBAL}]\n", true},
		{"implicit-global-provider-backedge", "proxy-providers: {P: {type: file, path: ./fake.yaml, proxy: GLOBAL}}\n", true},
		{"pass-rule-is-native-terminal", unifiedProxyTestNodes + "proxy-groups: [{name: C, type: select, proxies: [PASS-RULE, SYN-A]}]\n", false},
		{"dynamic-override-expression-fails-closed", "proxy-providers: {P: {type: file, path: ./fake.yaml, override: {override-expr: [dynamic]}}}\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := unifiedProxyTestRoot(t, tc.source)
			before := unifiedProxyTestBytes(t, root)
			err := ensureUnifiedProxy(root)
			if (err != nil) != tc.reject {
				t.Fatalf("dependency result rejected=%v want=%v", err != nil, tc.reject)
			}
			if tc.reject && !bytes.Equal(before, unifiedProxyTestBytes(t, root)) {
				t.Fatal("failed generation changed input")
			}
		})
	}
}

func TestUnifiedProxyOpenFallbacksAvoidNativeGroupReuse(t *testing.T) {
	for _, name := range []string{"PROXY", "UnknownBrand"} {
		for _, fallback := range []string{"''", "DIRECT", "COMPATIBLE"} {
			t.Run(name+fallback, func(t *testing.T) {
				source := "proxy-providers: {P: {type: file, path: ./fake.yaml}}\nproxy-groups: [{name: " + name + ", type: select, use: [P], empty-fallback: " + fallback + "}]\n"
				root := unifiedProxyTestGenerate(t, source)
				alias := unifiedProxyTestAlias(t, root)
				if len(alias.Proxies) != 0 || len(alias.Use) != 1 || alias.Use[0] != "P" {
					t.Fatal("explicit open empty fallback was reused")
				}
			})
		}
	}
}

func TestUnifiedProxySingleVlessEncodingsAndYamlMetadata(t *testing.T) {
	a := testApplication(t)
	link := []byte("vless://00000000-0000-4000-8000-000000000001@127.0.0.1:9?security=none&type=tcp#synthetic")
	inputs := [][]byte{link}
	for _, codec := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		inputs = append(inputs, []byte(codec.EncodeToString(link)))
	}
	for i, input := range inputs {
		converted, err := a.normalizeProfileInput(input, nil)
		if err != nil {
			t.Fatalf("single-node encoding %d failed", i)
		}
		root := unifiedProxyTestRoot(t, string(converted))
		if err := ensureUnifiedProxy(root); err != nil {
			t.Fatal("single-node alias failed")
		}
	}
	full := []byte(unifiedSyntheticConfig("NODE", "") + "metadata: vless://not-a-subscription\n")
	copied, err := a.normalizeProfileInput(full, nil)
	if err != nil || !bytes.Equal(copied, full) {
		t.Fatal("full yaml was converted because metadata contained a link")
	}
}
