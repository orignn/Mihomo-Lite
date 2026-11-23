package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func overrideFormLog(value string) *string       { return &value }
func overrideFormDNS(values ...string) *[]string { return &values }

func overrideFormConvert(t *testing.T, req overrideFormRequest) overrideFormResponse {
	t.Helper()
	result, err := convertOverrideForm(req)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOverrideFormSimpleLayerInheritance(t *testing.T) {
	for _, text := range []string{"", " \r\n\t", "{}\r\n", "dns: {}\n", "dns: {enable: false, fallback: [9.9.9.9]}\n"} {
		t.Run(strings.ReplaceAll(text, "\n", "_"), func(t *testing.T) {
			simple, err := readOverrideSimple(text)
			if err != nil || simple.LogLevel != "" || len(simple.DNSServers) != 0 || simple.DNSServers == nil || !simple.LogEditable || !simple.DNSEditable {
				t.Fatalf("inheritance not represented: %+v, %v", simple, err)
			}
			result := overrideFormConvert(t, overrideFormRequest{YAML: text, LogLevel: overrideFormLog(""), DNSServers: overrideFormDNS()})
			if result.YAML != text {
				t.Fatal("no-op did not preserve original bytes")
			}
		})
	}
	simple, err := readOverrideSimple("log-level: warning\ndns: {nameserver: [1.1.1.1, 'https://dns.example.test/dns-query']}\n")
	if err != nil || simple.LogLevel != "warning" || !simple.LogEditable || !simple.DNSEditable || simple.HasAdvanced || len(simple.DNSServers) != 2 {
		t.Fatal("simple values missing", err)
	}
	for _, level := range []string{"debug", "info", "warning", "error", "silent"} {
		result := overrideFormConvert(t, overrideFormRequest{LogLevel: overrideFormLog(level)})
		if result.Simple.LogLevel != level || len(result.Simple.DNSServers) != 0 {
			t.Fatal("log enum conversion failed")
		}
	}
}

func TestOverrideFormPreservesNestedUnknownAndArrays(t *testing.T) {
	text := "# SYNTHETIC configuration\nlog-level: warning # keep log comment\ndns:\n  enable: false\n  nameserver: [1.1.1.1]\n  fallback: [9.9.9.9, 'tls://dns.example.test']\n  fallback-filter: {geoip: false, domain: ['+.example.test']}\n  x-unknown: {nested: [one, {two: three}]}\nx-unknown:\n  map: {nested: [one, two]}\nrules: ['MATCH,DIRECT']\ntun: {enable: false}\n"
	before, err := decodeOverrideYAML(text)
	if err != nil {
		t.Fatal(err)
	}
	result := overrideFormConvert(t, overrideFormRequest{YAML: text, DNSServers: overrideFormDNS("[2001:db8::53]:5353", "https://dns.example.test/dns-query")})
	after, err := decodeOverrideYAML(result.YAML)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"log-level", "x-unknown", "rules", "tun"} {
		var a, b any
		if mappingValue(before, key).Decode(&a) != nil || mappingValue(after, key).Decode(&b) != nil || !reflect.DeepEqual(a, b) {
			t.Fatalf("unrelated key changed: %s", key)
		}
	}
	var beforeDNS, afterDNS map[string]any
	_ = mappingValue(before, "dns").Decode(&beforeDNS)
	_ = mappingValue(after, "dns").Decode(&afterDNS)
	delete(beforeDNS, "nameserver")
	delete(afterDNS, "nameserver")
	if !reflect.DeepEqual(beforeDNS, afterDNS) || !result.Simple.HasAdvanced || !strings.Contains(result.YAML, "keep log comment") {
		t.Fatal("unrelated DNS fields/comments changed")
	}
	cleared := overrideFormConvert(t, overrideFormRequest{YAML: result.YAML, DNSServers: overrideFormDNS()})
	root, _ := decodeOverrideYAML(cleared.YAML)
	dns := mappingValue(root, "dns")
	if dns == nil || mappingValue(dns, "nameserver") != nil || mappingValue(dns, "enable").Value != "false" || mappingValue(dns, "fallback") == nil {
		t.Fatal("DNS clear damaged enable/fallback")
	}
	logCleared := overrideFormConvert(t, overrideFormRequest{YAML: text, LogLevel: overrideFormLog("")})
	root, _ = decodeOverrideYAML(logCleared.YAML)
	if mappingValue(root, "log-level") != nil || mappingValue(mappingValue(root, "dns"), "nameserver").Content[0].Value != "1.1.1.1" {
		t.Fatal("log clear changed unspecified DNS")
	}
}

func TestOverrideFormClearsOnlyEmptyDNSContainer(t *testing.T) {
	result := overrideFormConvert(t, overrideFormRequest{YAML: "log-level: info\ndns: {nameserver: [1.1.1.1]}\n", DNSServers: overrideFormDNS()})
	root, _ := decodeOverrideYAML(result.YAML)
	if mappingValue(root, "dns") != nil || mappingValue(root, "log-level").Value != "info" {
		t.Fatal("empty DNS container not cleaned")
	}
	result = overrideFormConvert(t, overrideFormRequest{YAML: "dns: {nameserver: [1.1.1.1]}\n", LogLevel: overrideFormLog(""), DNSServers: overrideFormDNS()})
	root, _ = decodeOverrideYAML(result.YAML)
	if len(root.Content) != 0 || result.Simple.LogLevel != "" || len(result.Simple.DNSServers) != 0 {
		t.Fatal("empty layer conversion failed")
	}
	result = overrideFormConvert(t, overrideFormRequest{DNSServers: overrideFormDNS("1.1.1.1")})
	root, _ = decodeOverrideYAML(result.YAML)
	if mappingValue(mappingValue(root, "dns"), "enable") != nil {
		t.Fatal("form invented dns.enable")
	}
}

func TestOverrideFormComplexFieldsAreIndependentlyReadOnly(t *testing.T) {
	cases := []struct {
		name, text string
		log, dns   bool
	}{
		{"root-anchor", "&root {log-level: info, dns: {nameserver: [1.1.1.1]}}\n", false, false},
		{"log-key-anchor", "&key log-level: info\nx: *key\n", false, true},
		{"dns-key-anchor", "&key dns: {nameserver: [1.1.1.1]}\nx: *key\n", true, false},
		{"dns-nameserver-key-anchor", "dns: {&key nameserver: [1.1.1.1]}\nx: *key\n", true, false},
		{"merge-sequence", "x: &a {log-level: warning}\ny: &b {dns: {nameserver: [1.1.1.1]}}\n<<: [*a, *b]\n", false, false},
		{"dns-custom-container", "dns: !custom {nameserver: [1.1.1.1]}\n", true, false},
		{"log-custom-scalar", "log-level: !custom info\n", false, true},
		{"log-alias", "x: &level warning\nlog-level: *level\n", false, true},
		{"log-anchor", "log-level: &level warning\nx: *level\n", false, true},
		{"log-merge", "x: &base {log-level: warning}\n<<: *base\n", false, true},
		{"log-explicit-over-merge", "x: &base {log-level: warning}\n<<: *base\nlog-level: info\n", false, true},
		{"dns-alias", "x: &dns {nameserver: [1.1.1.1]}\ndns: *dns\n", true, false},
		{"dns-anchor", "dns: &dns {nameserver: [1.1.1.1]}\nx: *dns\n", true, false},
		{"dns-root-merge", "x: &base {dns: {nameserver: [1.1.1.1]}}\n<<: *base\n", true, false},
		{"dns-merge", "x: &dns {nameserver: [1.1.1.1]}\ndns: {<<: *dns}\n", true, false},
		{"dns-array-alias", "x: &ns [1.1.1.1]\ndns: {nameserver: *ns}\n", true, false},
		{"dns-array-anchor", "dns: {nameserver: &ns [1.1.1.1]}\nx: *ns\n", true, false},
		{"dns-item-alias", "x: &ip 1.1.1.1\ndns: {nameserver: [*ip]}\n", true, false},
		{"dns-item-anchor", "dns: {nameserver: [&ip 1.1.1.1]}\nx: *ip\n", true, false},
		{"dns-scalar", "dns: disabled\n", true, false},
		{"dns-map-array", "dns: {nameserver: {primary: 1.1.1.1}}\n", true, false},
		{"dns-empty-replacement", "dns: {nameserver: []}\n", true, false},
		{"dns-typed-item", "dns: {nameserver: [false]}\n", true, false},
		{"dns-custom-item", "dns: {nameserver: [!custom 1.1.1.1]}\n", true, false},
		{"log-empty", "log-level: ''\n", false, true},
		{"log-unknown", "log-level: verbose\n", false, true},
		{"log-mapping", "log-level: {nested: info}\n", false, true},
		{"unrelated-alias", "x: &other {nested: [one, two]}\ny: *other\n", true, true},
		{"unrelated-merge", "x: &other {nested: [one, two]}\n<<: *other\n", true, true},
		{"unrelated-dns-merge", "x: &other {fallback: [9.9.9.9], enable: false}\ndns: {<<: *other, nameserver: [1.1.1.1]}\n", true, true},
		{"unrelated-dns-anchor", "dns: {fallback: &fb [9.9.9.9], nameserver: [1.1.1.1]}\nx: *fb\n", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			simple, err := readOverrideSimple(c.text)
			if err != nil || simple.LogEditable != c.log || simple.DNSEditable != c.dns || !simple.HasAdvanced {
				t.Fatalf("editable flags: %+v err=%v", simple, err)
			}
			noop := overrideFormConvert(t, overrideFormRequest{YAML: c.text})
			if noop.YAML != c.text {
				t.Fatal("complex no-op altered syntax")
			}
			for _, clear := range []bool{false, true} {
				log := "debug"
				dns := overrideFormDNS("8.8.8.8")
				if clear {
					log = ""
					dns = overrideFormDNS()
				}
				_, logErr := convertOverrideForm(overrideFormRequest{YAML: c.text, LogLevel: &log})
				_, dnsErr := convertOverrideForm(overrideFormRequest{YAML: c.text, DNSServers: dns})
				if (logErr == nil) != c.log || (dnsErr == nil) != c.dns {
					t.Fatalf("field-local protection failed: log=%v dns=%v", logErr, dnsErr)
				}
			}
			if c.log || c.dns {
				req := overrideFormRequest{YAML: c.text}
				if c.log {
					req.LogLevel = overrideFormLog("debug")
				} else {
					req.DNSServers = overrideFormDNS("8.8.8.8")
				}
				result := overrideFormConvert(t, req)
				var original, generated yaml.Node
				_ = yaml.Unmarshal([]byte(c.text), &original)
				_ = yaml.Unmarshal([]byte(result.YAML), &generated)
				// The other managed subtree and advanced extensions retain syntax/values.
				old := original.Content[0]
				next := generated.Content[0]
				for _, key := range []string{"x", "y"} {
					if node := mappingValue(old, key); node != nil {
						a, _ := yaml.Marshal(node)
						b, _ := yaml.Marshal(mappingValue(next, key))
						if !bytes.Equal(a, b) {
							t.Fatal("unrelated aliases/extensions rewritten")
						}
					}
				}
			}
		})
	}
}

func TestOverrideFormNoOpPreservesBytes(t *testing.T) {
	text := "# keep CRLF\r\nlog-level: 'warning'\r\ndns: {nameserver: [1.1.1.1]}\r\nx: {a: [1, 2]}\r\n"
	for _, req := range []overrideFormRequest{{YAML: text}, {YAML: text, LogLevel: overrideFormLog("warning")}, {YAML: text, DNSServers: overrideFormDNS("1.1.1.1")}, {YAML: text, LogLevel: overrideFormLog("warning"), DNSServers: overrideFormDNS("1.1.1.1")}} {
		if overrideFormConvert(t, req).YAML != text {
			t.Fatal("no-op rewrote comments/CRLF/quoting")
		}
	}
}

func TestOverrideFormDNSAddressSyntaxAndLimits(t *testing.T) {
	valid := []string{"1.1.1.1", "192.0.2.53:5353", "2001:db8::53", "[2001:db8::53]:5353", "https://dns.example.test/dns-query", "https://dns.example.test:8443/dns-query?mode=test", "https://[2001:db8::53]/dns-query", "tls://dns.example.test", "tls://[2001:db8::53]:853", "tcp://192.0.2.53:5353", "udp://[2001:db8::53]", "system"}
	for _, value := range valid {
		if !validOverrideDNSAddress(value) {
			t.Fatalf("valid SYNTHETIC DNS rejected: %s", value)
		}
	}
	invalid := []string{"", " ", " 1.1.1.1", "1.1.1.1 ", "1.1.1.1\nlog-level: debug", "1.1.1.1\r", "1.1.1.1\t", "1.1.1.1\u0000", "1.1.1.1 ", "dns.example.test", "999.1.1.1", "1.1.1.1:0", "1.1.1.1:65536", "1.1.1.1:-53", "1.1.1.1:+53", "1.1.1.1:x", "[2001:db8::53]:", "http://dns.example.test/dns-query", "ftp://dns.example.test", "https://", "https://user@dns.example.test/dns-query", "https://dns.example.test/#proxy", "https://dns.example.test:", "https://dns.example.test:65536", "https://bad_host/dns-query", "https://[bad.test]/dns-query", "https://2001:db8::53/dns-query", "https://999.1.1.1/dns-query", "https://dns.example.test/%0aevil", "https://dns.example.test/?x=%0devil", "https://dns.example.test/a b", "https://dns.example.test/\\evil", "tls://dns.example.test/path", "tls://dns.example.test?option=1", "udp://dns.example.test", "system://", "SYSTEM"}
	for i, value := range invalid {
		t.Run(strconvOverrideFormTestIndex(i), func(t *testing.T) {
			if validOverrideDNSAddress(value) {
				t.Fatal("invalid DNS accepted")
			}
			_, err := convertOverrideForm(overrideFormRequest{DNSServers: overrideFormDNS(value)})
			if err == nil {
				t.Fatal("invalid DNS converted")
			}
		})
	}
	if validOverrideDNSList(make([]string, maxOverrideDNSServers+1)) {
		t.Fatal("DNS count limit ignored")
	}
	if validOverrideDNSAddress("https://dns.example.test/" + strings.Repeat("a", maxOverrideDNSAddressBytes)) {
		t.Fatal("DNS address size limit ignored")
	}
	many := make([]string, maxOverrideDNSServers)
	for i := range many {
		many[i] = "https://dns.example.test/" + strings.Repeat("a", 1100)
	}
	if validOverrideDNSList(many) {
		t.Fatal("DNS total size limit ignored")
	}
}

func strconvOverrideFormTestIndex(i int) string { return strconv.Itoa(i) }

func TestOverrideFormStrictJSON(t *testing.T) {
	valid := []string{`{"yaml":""}`, `{"yaml":"{}","logLevel":"","dnsServers":[]}`, `{"yaml":"{}","logLevel":"silent","dnsServers":["1.1.1.1"]}`, `{ "yaml" : "{}", "dnsServers" : [ "system" ] }`}
	for _, body := range valid {
		if _, err := decodeOverrideFormRequest([]byte(body)); err != nil {
			t.Fatal("valid JSON rejected", err)
		}
	}
	invalid := []string{`{}`, `null`, `[]`, `{"yaml":null}`, `{"yaml":1}`, `{"yaml":{}}`, `{"yaml":"","logLevel":null}`, `{"yaml":"","logLevel":false}`, `{"yaml":"","logLevel":"INFO"}`, `{"yaml":"","logLevel":" warning"}`, `{"yaml":"","logLevel":"verbose"}`, `{"yaml":"","dnsServers":null}`, `{"yaml":"","dnsServers":"1.1.1.1"}`, `{"yaml":"","dnsServers":[null]}`, `{"yaml":"","dnsServers":[1]}`, `{"yaml":"","dnsServers":[{}]}`, `{"yaml":"","dnsServers":[""]}`, `{"yaml":"","dnsServers":["1.1.1.1\nlog-level: debug"]}`, `{"yaml":"","unknown":false}`, `{"yaml":"","scope":"global"}`, `{"YAML":""}`, `{"yaml":"","LogLevel":"info"}`, `{"yaml":"","y\u0061ml":"{}"}`, `{"yaml":"","logLevel":"info","logLevel":"error"}`, `{"yaml":"","dnsServers":[],"dnsServers":[]}`, `{"yaml":"","y\u0061ml":"{}"}`, `{"yaml":""}{}`, `{"yaml":""}null`, `{"yaml":"",}`, `{"yaml":""`}
	for i, body := range invalid {
		t.Run(strconvOverrideFormTestIndex(i), func(t *testing.T) {
			if _, err := decodeOverrideFormRequest([]byte(body)); err == nil {
				t.Fatal("malformed JSON accepted")
			}
		})
	}
	badUTF8 := []byte{'{', '"', 'y', 'a', 'm', 'l', '"', ':', '"', 0xff, '"', '}'}
	if _, err := decodeOverrideFormRequest(badUTF8); err == nil {
		t.Fatal("invalid UTF-8 JSON accepted")
	}
}

func TestOverrideFormCannotBypassStrictYAML(t *testing.T) {
	invalid := []string{"# comment only", "[]", "dns: null", "dns: {nameserver: [null]}", "x: null", "log-level: info\nlog-level: error", "x: {a: 1, a: 2}", "{}\n---\nlog-level: info", "? [one, two]\n: three", "1: true", "x: &loop {nested: *loop}", unifiedProxyMarker + ": {}", "x: &base {" + unifiedProxyMarker + ": {}}\n<<: *base", strings.Repeat(" ", maxOverrideYAMLBytes+1), string([]byte{0xff})}
	for i, text := range invalid {
		t.Run(strconvOverrideFormTestIndex(i), func(t *testing.T) {
			for _, req := range []overrideFormRequest{{YAML: text}, {YAML: text, LogLevel: overrideFormLog("info")}, {YAML: text, DNSServers: overrideFormDNS("1.1.1.1")}} {
				if _, err := convertOverrideForm(req); err == nil {
					t.Fatal("unsafe YAML accepted via form")
				}
			}
		})
	}
}

func TestOverrideFormHTTPPureConversionAndSecurity(t *testing.T) {
	// No initialized Settings/Events/Proxy/core: any lifecycle call would fail.
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel.txt")
	if os.WriteFile(sentinel, []byte("SYNTHETIC-unchanged"), 0600) != nil {
		t.Fatal("fixture write failed")
	}
	a := &Application{baseDir: dir, dataDir: dir, logsDir: dir}
	handler := a.Routes()
	body := `{"yaml":"log-level: warning","dnsServers":["1.1.1.1"]}`
	for _, c := range []struct {
		host, origin, marker string
		status               int
	}{
		{"127.0.0.1:17653", "", "1", 200}, {"127.0.0.1:17653", "http://127.0.0.1:17653", "1", 200},
		{"127.0.0.1:17653", "", "", 403}, {"127.0.0.1:17653", "https://example.invalid", "1", 403}, {"example.invalid", "", "1", 403},
	} {
		r := httptest.NewRequest(http.MethodPost, "http://"+c.host+"/api/overrides/form", strings.NewReader(body))
		if c.marker != "" {
			r.Header.Set("X-Mihomo-Lite", c.marker)
		}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != c.status || a.operation || a.core != nil || a.profileRevision.Load() != 0 {
			t.Fatalf("pure/security contract status=%d want=%d", w.Code, c.status)
		}
		if w.Code == 200 {
			var result overrideFormResponse
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Simple.LogLevel != "warning" || len(result.Simple.DNSServers) != 1 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("success response contract failed")
			}
		}
	}
	for _, bad := range []string{`{"yaml":null}`, `{"yaml":"dns: null"}`, strings.Repeat(" ", maxOverrideYAMLBytes*6+1)} {
		w := httptest.NewRecorder()
		a.OverrideForm(w, overrideHTTPRequest(http.MethodPost, "/api/overrides/form", strings.NewReader(bad)))
		if w.Code != 400 {
			t.Fatal("bad body accepted")
		}
	}
	w := httptest.NewRecorder()
	a.OverrideForm(w, httptest.NewRequest(http.MethodGet, "/api/overrides/form", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Fatal("method contract failed")
	}
	items, _ := os.ReadDir(dir)
	data, _ := os.ReadFile(sentinel)
	if len(items) != 1 || string(data) != "SYNTHETIC-unchanged" {
		t.Fatal("conversion wrote application data")
	}
}

func TestOverrideFormGETSimpleUsesSelectedLayerOnly(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, "")
	store := emptyOverrideStore()
	store.Global = "log-level: warning\ndns: {nameserver: [1.1.1.1]}"
	store.Profiles[overrideFixtureID1] = "dns: {fallback: [9.9.9.9]}"
	storeOverrideFixture(t, a, store)
	before := overrideReadFixture(t, a.overridesPath())
	for _, c := range []struct {
		query, log string
		dns        int
		advanced   bool
	}{
		{"?scope=global", "warning", 1, false}, {"?scope=profile&profileId=" + overrideFixtureID1, "", 0, true},
	} {
		w := httptest.NewRecorder()
		a.Overrides(w, overrideHTTPRequest(http.MethodGet, "/api/overrides"+c.query, nil))
		var result struct {
			YAML       string         `json:"yaml"`
			Simple     overrideSimple `json:"simple"`
			Precedence []string       `json:"precedence"`
			Warning    string         `json:"warning"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Simple.LogLevel != c.log || len(result.Simple.DNSServers) != c.dns || !result.Simple.LogEditable || !result.Simple.DNSEditable || result.Simple.HasAdvanced != c.advanced || len(result.Precedence) == 0 || result.Warning == "" {
			t.Fatal("GET layer/legacy contract failed")
		}
	}
	if !bytes.Equal(before, overrideReadFixture(t, a.overridesPath())) {
		t.Fatal("GET persisted data")
	}
	store.Global = "x: &ns [1.1.1.1]\ndns: {nameserver: *ns}"
	storeOverrideFixture(t, a, store)
	w := httptest.NewRecorder()
	a.Overrides(w, overrideHTTPRequest(http.MethodGet, "/api/overrides?scope=global", nil))
	var result struct {
		Simple overrideSimple `json:"simple"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Simple.DNSEditable || len(result.Simple.DNSServers) != 1 {
		t.Fatal("GET alias misrepresented as editable/inheritance")
	}
}
