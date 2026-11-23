package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRuleSimulationPartialMetadataAndOrder(t *testing.T) {
	makeRule := func(kind, value string) CustomRule {
		return CustomRule{ID: kind, Type: kind, Value: value, Policy: "DIRECT", Enabled: true}
	}
	domain := makeRule("DOMAIN-SUFFIX", "example.com")
	process := makeRule("PROCESS-NAME", "app.exe")
	pathRule := makeRule("PROCESS-PATH", "C:/Apps/app.exe")
	geoSite := makeRule("GEOSITE", "cn")
	geoIP := makeRule("GEOIP", "CN")
	cidr := makeRule("IP-CIDR", "192.0.2.0/24")
	noResolve := cidr
	noResolve.NoResolve = true
	noResolveGeo := geoIP
	noResolveGeo.NoResolve = true
	for _, tc := range []struct {
		name, query, kind, status string
		rules                     []CustomRule
		uncertain                 []string
		candidate                 string
	}{
		{"domain first", "a.example.com", "destination", "matched", []CustomRule{domain, geoSite, process}, nil, "DOMAIN-SUFFIX"},
		{"geo before domain", "a.example.com", "destination", "unknown", []CustomRule{geoSite, domain}, []string{"GEOSITE"}, "DOMAIN-SUFFIX"},
		{"geo only", "a.example.com", "destination", "unknown", []CustomRule{geoSite}, []string{"GEOSITE"}, ""},
		{"geoip before ip", "192.0.2.4", "destination", "unknown", []CustomRule{geoIP, cidr}, []string{"GEOIP"}, "IP-CIDR"},
		{"dns before domain", "a.example.com", "destination", "unknown", []CustomRule{cidr, domain}, []string{"IP-CIDR"}, "DOMAIN-SUFFIX"},
		{"no-resolve skips cidr dns", "a.example.com", "destination", "matched", []CustomRule{noResolve, domain}, nil, "DOMAIN-SUFFIX"},
		{"geoip no-resolve is not serialized", "a.example.com", "destination", "unknown", []CustomRule{noResolveGeo, domain}, []string{"GEOIP"}, "DOMAIN-SUFFIX"},
		{"IPv4-mapped address normalized", "::ffff:192.0.2.4", "destination", "matched", []CustomRule{cidr}, nil, "IP-CIDR"},
		{"IPv6 zone does not change membership", "fe80::1%Ethernet", "destination", "matched", []CustomRule{makeRule("IP-CIDR6", "fe80::/10")}, nil, "IP-CIDR6"},
		{"missing process before domain", "a.example.com", "destination", "unknown", []CustomRule{process, pathRule, domain}, []string{"PROCESS-NAME", "PROCESS-PATH"}, "DOMAIN-SUFFIX"},
		{"missing destination before process", "app.exe", "process", "unknown", []CustomRule{domain, cidr, process}, []string{"DOMAIN-SUFFIX", "IP-CIDR"}, "PROCESS-NAME"},
		{"missing process path", "app.exe", "process", "unknown", []CustomRule{pathRule, process}, []string{"PROCESS-PATH"}, "PROCESS-NAME"},
		{"full path provides process name", "C:/Apps/app.exe", "process", "matched", []CustomRule{process, pathRule}, nil, "PROCESS-NAME"},
		{"full path priority", "C:/Apps/app.exe", "process", "matched", []CustomRule{pathRule, process}, nil, "PROCESS-PATH"},
		{"disabled geo", "a.example.com", "destination", "matched", []CustomRule{{ID: "disabled", Type: "GEOSITE", Value: "cn", Enabled: false}, domain}, nil, "DOMAIN-SUFFIX"},
		{"literal ip has no host", "192.0.2.4", "destination", "matched", []CustomRule{domain, geoSite, cidr}, nil, "IP-CIDR"},
		{"no deterministic or unknown rule", "different.example", "destination", "no-match", []CustomRule{domain}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := simulateCustomRules(tc.rules, tc.query, tc.kind)
			if err != nil || got.Status != tc.status || !reflect.DeepEqual(got.UncertainTypes, tc.uncertain) {
				t.Fatalf("result=%+v, err=%v", got, err)
			}
			hit := got.Matched
			if tc.status == "unknown" {
				hit = got.PossibleMatch
				if got.Matched != nil {
					t.Fatal("unknown must not claim a definite match")
				}
			}
			if (hit == nil && tc.candidate != "") || (hit != nil && hit.Type != tc.candidate) {
				t.Fatalf("candidate=%+v", hit)
			}
		})
	}
}

func TestRuleSimulationDomainNormalization(t *testing.T) {
	for _, tc := range []struct{ name, ruleType, value, query, status string }{
		{"IDN exact", "DOMAIN", "例子.example", "例子.example.", "matched"},
		{"IDN suffix", "DOMAIN-SUFFIX", "例子.example", "a.例子.example", "matched"},
		{"case and root dot", "DOMAIN", "EXAMPLE.COM", "Example.Com.", "matched"},
		{"suffix label boundary", "DOMAIN-SUFFIX", "example.com", "notexample.com", "no-match"},
		{"suffix superdomain", "DOMAIN-SUFFIX", "a.example.com", "example.com", "no-match"},
		{"keyword case", "DOMAIN-KEYWORD", "EXAMPLE", "Example.com", "matched"},
		{"Unicode keyword uses canonical host", "DOMAIN-KEYWORD", "例子", "例子.example", "no-match"},
		{"exe domain not inferred process", "DOMAIN", "app.exe", "app.exe", "matched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := CustomRule{ID: "test", Type: tc.ruleType, Value: tc.value, Policy: "DIRECT", Enabled: true}
			if err := validateCustomRule(&rule); err != nil {
				t.Fatal(err)
			}
			got, err := simulateCustomRules([]CustomRule{rule}, tc.query, "destination")
			if err != nil || got.Status != tc.status {
				t.Fatalf("result=%+v, err=%v", got, err)
			}
		})
	}
}

func TestRuleTestRejectsInvalidInputs(t *testing.T) {
	a := testApplication(t)
	for _, tc := range []struct{ query, kind string }{
		{"", "destination"}, {"https://example.com/path", "destination"}, {"a..example", "destination"},
		{"example.com:443", "destination"}, {strings.Repeat("a", 64) + ".example", "destination"},
		{"app.exe", "invalid"}, {"relative/path/app.exe", "process"}, {"bad:name", "process"},
		{strings.Repeat("a", 2049), "destination"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/rules/test?q="+url.QueryEscape(tc.query)+"&kind="+tc.kind, nil)
		w := httptest.NewRecorder()
		a.RuleTest(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid input accepted: status=%d", w.Code)
		}
	}
}

func TestRuleTestReadsSavedRulesAndOnlyHintsDatabasePresence(t *testing.T) {
	a := testApplication(t)
	if err := os.MkdirAll(a.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	saved := []CustomRule{{ID: "geo", Type: "GEOSITE", Value: "cn", Policy: "REJECT", Enabled: true}}
	data, _ := json.Marshal(saved)
	if err := os.WriteFile(a.customRulesPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	request := func() RuleTestResult {
		r := httptest.NewRequest(http.MethodGet, "/api/rules/test?q=example.com&kind=destination", nil)
		w := httptest.NewRecorder()
		a.RuleTest(w, r)
		var got RuleTestResult
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("rule API status=%d", w.Code)
		}
		return got
	}
	for _, stage := range []string{"missing", "empty", "present"} {
		if stage != "missing" {
			value := []byte{}
			if stage == "present" {
				value = []byte("synthetic presence, not a valid database")
			}
			if err := os.WriteFile(filepath.Join(a.dataDir, "GeoSite.dat"), value, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got := request()
		if got.Status != "unknown" || got.Matched != nil {
			t.Fatalf("must remain unknown even when data exists: %+v", got)
		}
		if (stage == "present" && len(got.MissingDatabases) != 0) || (stage != "present" && !reflect.DeepEqual(got.MissingDatabases, []string{"GEOSITE"})) {
			t.Fatalf("presence hint stage=%s: %+v", stage, got)
		}
	}
	if current, err := os.ReadFile(a.customRulesPath()); err != nil || string(current) != string(data) {
		t.Fatal("rule test changed saved rules")
	}
}
