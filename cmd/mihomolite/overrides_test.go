package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

const overrideFixtureID1 = "1111111111111111111111111111111111111111111111111111111111111111"
const overrideFixtureID2 = "2222222222222222222222222222222222222222222222222222222222222222"
const overrideNodeYAML = "proxies: [{name: SYNTHETIC, type: ss, server: 192.0.2.1, port: 443, cipher: aes-128-gcm, password: SYNTHETIC-NODE-SECRET}]\nproxy-groups: [{name: TEST, type: select, proxies: [SYNTHETIC]}]\nrules: ['MATCH,TEST']\n"

func overrideHTTPRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Header.Set("X-Mihomo-Lite", "1")
	return r
}

func overrideFixtureApp(t *testing.T, first, second string) *Application {
	t.Helper()
	a := newProfileTestApp(t)
	a.proxy = httpTestProxy{}
	a.events = NewEventHub()
	a.events.current = a.State
	a.config.APISecret = "SYNTHETIC-LAUNCHER-API-SECRET"
	store := profileStore{ActiveID: overrideFixtureID1}
	for i, item := range []struct{ id, text string }{{overrideFixtureID1, first}, {overrideFixtureID2, second}} {
		if item.text == "" {
			continue
		}
		if err := atomicWrite(a.profileContentPath(item.id), []byte(item.text), 0o600); err != nil {
			t.Fatal(err)
		}
		store.Profiles = append(store.Profiles, Profile{ID: item.id, Name: []string{"Synthetic first", "Synthetic second"}[i], Source: "local"})
	}
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "source.yaml"), []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	return a
}
func storeOverrideFixture(t *testing.T, a *Application, store overrideStore) {
	t.Helper()
	data, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(a.overridesPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func overrideReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func overrideRenderRoot(t *testing.T, data []byte) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) != 1 {
		t.Fatal("invalid synthetic render")
	}
	return doc.Content[0]
}

func TestOverridesStrictYAML(t *testing.T) {
	cases := []struct {
		name, text string
		ok         bool
	}{
		{"empty", "", true}, {"comment-only", "# inherit\n", false}, {"empty-map", "{}", true},
		{"mapping", "dns: {nameserver: [1.1.1.1]}", true}, {"sequence", "[dns]", false},
		{"null", "dns: null", false}, {"nested-null", "dns: {nameserver: [null]}", false},
		{"duplicate", "log-level: warning\nlog-level: debug", false},
		{"multiple-documents", "dns: {}\n---\nlog-level: info", false},
		{"aliases", "x: &dns {nameserver: [1.1.1.1]}\ndns: *dns", true},
		{"recursive-alias", "x: &loop {nested: *loop}", false},
		{"complex-key", "? [one, two]\n: three", false},
		{"non-string-key", "1: true", false},
		{"owner-marker", unifiedProxyMarker + ": {}", false},
		{"invalid-utf8", string([]byte{0xff}), false},
		{"too-large", strings.Repeat(" ", maxOverrideYAMLBytes+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeOverrideYAML(tc.text)
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%t expected=%t", err == nil, tc.ok)
			}
		})
	}
}
func TestOverridesStoreContracts(t *testing.T) {
	empty, err := normalizeOverrideStore(nil)
	if err != nil || empty.SchemaVersion != 1 || empty.Profiles == nil {
		t.Fatal("legacy empty layer")
	}
	for _, bad := range []overrideStore{{SchemaVersion: 1, Profiles: map[string]string{strings.Repeat("A", 64): "{}"}}, {SchemaVersion: 2}, {SchemaVersion: 1, Profiles: map[string]string{"../escape": "{}"}}, {SchemaVersion: 1, Global: "dns: null"}} {
		if _, err := normalizeOverrideStore(&bad); err == nil {
			t.Fatal("invalid store accepted")
		}
	}
	valid := emptyOverrideStore()
	valid.Profiles[overrideFixtureID1] = "log-level: debug"
	copy, err := normalizeOverrideStore(&valid)
	if err != nil {
		t.Fatal(err)
	}
	copy.Profiles[overrideFixtureID1] = "log-level: error"
	if valid.Profiles[overrideFixtureID1] != "log-level: debug" {
		t.Fatal("candidate aliases original store")
	}
}
func TestOverridesStrictStoreJSONAndOrphanPruning(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, "")
	invalid := []string{
		`{"schemaVersion":1,"schemaVersion":1,"global":"","profiles":{}}`,
		`{"schemaVersion":1,"global":null,"profiles":{}}`,
		`{"schemaVersion":1,"global":"","profiles":null}`,
		`{"schemaVersion":1,"Global":"","profiles":{}}`,
		`{"schemaVersion":1,"global":"","profiles":{"` + overrideFixtureID1 + `":"{}","` + overrideFixtureID1 + `":"{}"}}`,
	}
	for _, data := range invalid {
		if err := atomicWrite(a.overridesPath(), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := a.loadOverrides(); err == nil {
			t.Fatal("invalid persisted override JSON accepted")
		}
	}
	store := emptyOverrideStore()
	store.Profiles[overrideFixtureID2] = "log-level: debug"
	storeOverrideFixture(t, a, store)
	before := overrideReadFixture(t, a.overridesPath())
	if _, err := a.saveOverrides(context.Background(), "global", "", "dns: {listen: 53}"); err == nil {
		t.Fatal("invalid candidate accepted")
	}
	if !bytes.Equal(before, overrideReadFixture(t, a.overridesPath())) {
		t.Fatal("failed candidate deleted orphan layer")
	}
	if _, err := a.saveOverrides(context.Background(), "global", "", "log-level: warning"); err != nil {
		t.Fatal(err)
	}
	saved, err := a.loadOverrides()
	if err != nil || len(saved.Profiles) != 0 {
		t.Fatal("successful save did not prune obsolete ID")
	}
}

func TestOverridesRecursiveMappingArraysAndPrecedence(t *testing.T) {
	a := testApplication(t)
	source := []byte("proxies: []\ndns: {enable: true, nameserver: [1.1.1.1], fallback-filter: {geoip: false, domain: [original.test]}}\nhosts: {original.test: 192.0.2.1}\nlog-level: warning\nrules: ['MATCH,DIRECT']\n")
	original := append([]byte(nil), source...)
	store := emptyOverrideStore()
	store.Global = "dns: {nameserver: [8.8.8.8, 9.9.9.9], fallback-filter: {domain: [global.test]}}\nhosts: {global.test: 192.0.2.2}\nlog-level: info\n"
	store.Profiles[overrideFixtureID1] = "dns: {nameserver: [], fallback-filter: {geoip: true}}\nhosts: {profile.test: 192.0.2.3}\nlog-level: debug\n"
	_, data, err := a.prepareProfileConfig(source, overrideFixtureID1, a.previewRuntime(), []CustomRule{}, &store)
	if err != nil {
		t.Fatal(err)
	}
	root := overrideRenderRoot(t, data)
	dns := mappingValue(root, "dns")
	if mappingValue(dns, "enable").Value != "true" || len(mappingValue(dns, "nameserver").Content) != 0 {
		t.Fatal("mapping inheritance/array replacement failed")
	}
	filter := mappingValue(dns, "fallback-filter")
	if mappingValue(filter, "geoip").Value != "true" || mappingValue(filter, "domain").Content[0].Value != "global.test" {
		t.Fatal("nested map precedence failed")
	}
	if mappingValue(root, "log-level").Value != "debug" || len(mappingValue(root, "hosts").Content) != 6 {
		t.Fatal("scalar or hosts merge failed")
	}
	_, other, err := a.prepareProfileConfig(source, overrideFixtureID2, a.previewRuntime(), []CustomRule{}, &store)
	if err != nil {
		t.Fatal(err)
	}
	otherRoot := overrideRenderRoot(t, other)
	if mappingValue(otherRoot, "log-level").Value != "info" || len(mappingValue(mappingValue(otherRoot, "dns"), "nameserver").Content) != 2 {
		t.Fatal("profile layer leaked to another stable ID")
	}
	if !bytes.Equal(original, source) {
		t.Fatal("source YAML mutated")
	}
}
func TestOverridesSafetyCannotBeBypassed(t *testing.T) {
	a := testApplication(t)
	source := []byte("proxies: []\nrules: ['MATCH,DIRECT']\n")
	for _, listen := range []string{"0.0.0.0:1053", "[::]:1053", "192.0.2.9:1053", "localhost:1053", "[::1]:1053"} {
		t.Run(listen, func(t *testing.T) {
			store := emptyOverrideStore()
			store.Global = "allow-lan: true\nbind-address: '*'\nexternal-controller: 0.0.0.0:9090\nexternal-controller-tls: 0.0.0.0:9443\nexternal-controller-unix: evil.sock\nexternal-controller-pipe: evil\nexternal-ui-url: https://example.invalid/ui.zip\nsecret: OVERRIDE-NOT-API-SECRET\nport: 9999\nredir-port: 9998\ntproxy-port: 9997\ntun: {enable: true, auto-route: true}\ndns: {listen: '" + listen + "', enable: true, nameserver: [1.1.1.1]}\n"
			store.Profiles[overrideFixtureID1] = "allow-lan: true\nbind-address: 192.0.2.1\ntun: {enable: true}\n"
			_, data, err := a.prepareProfileConfig(source, overrideFixtureID1, a.previewRuntime(), []CustomRule{}, &store)
			if err != nil {
				t.Fatal(err)
			}
			root := overrideRenderRoot(t, data)
			if mappingValue(root, "allow-lan").Value != "false" || mappingValue(root, "bind-address").Value != "127.0.0.1" || !strings.HasPrefix(mappingValue(root, "external-controller").Value, "127.0.0.1:") {
				t.Fatal("loopback safety bypass")
			}
			for _, key := range []string{"port", "redir-port", "tproxy-port", "external-controller-tls", "external-controller-unix", "external-controller-pipe", "external-ui-url"} {
				if mappingValue(root, key) != nil {
					t.Fatalf("unsafe key retained: %s", key)
				}
			}
			if mappingValue(mappingValue(root, "tun"), "enable").Value != "false" || mappingValue(root, "secret").Value != overrideSecretPlaceholder {
				t.Fatal("TUN/API secret safety bypass")
			}
			want := "127.0.0.1:1053"
			if listen == "[::1]:1053" {
				want = listen
			}
			if mappingValue(mappingValue(root, "dns"), "listen").Value != want {
				t.Fatal("DNS inbound escaped loopback")
			}
		})
	}
	for _, text := range []string{"listeners: [{name: evil, type: mixed, port: 12345, listen: 0.0.0.0}]", "tunnels: [{network: tcp, address: 0.0.0.0:12345, target: example.test:443, proxy: DIRECT}]", "dns: {listen: 53}", "dns: {listen: unix://evil}", "dns: {listen: '0.0.0.0:65536'}"} {
		store := emptyOverrideStore()
		store.Global = text
		if _, _, err := a.prepareProfileConfig(source, "", a.previewRuntime(), []CustomRule{}, &store); err == nil {
			t.Fatal("unsupported inbound accepted")
		}
	}
}
func TestOverridesGlobalTransactionChecksInactiveProfiles(t *testing.T) {
	a := overrideFixtureApp(t, "proxies: []\nrules: ['MATCH,DIRECT']\n", testProfileYAML)
	initial := emptyOverrideStore()
	initial.Global = "log-level: warning"
	storeOverrideFixture(t, a, initial)
	before := overrideReadFixture(t, a.overridesPath())
	source := overrideReadFixture(t, a.sourceYAMLPath())
	second := overrideReadFixture(t, a.profileContentPath(overrideFixtureID2))
	count, err := a.saveOverrides(context.Background(), "global", "", "proxy-groups: [{name: OTHER, type: select, proxies: [DIRECT]}]")
	if err == nil || count != 0 {
		t.Fatal("inactive profile's invalid final rule accepted")
	}
	if !bytes.Equal(before, overrideReadFixture(t, a.overridesPath())) {
		t.Fatal("failed validation committed candidate")
	}
	count, err = a.saveOverrides(context.Background(), "global", "", "log-level: error")
	if err != nil || count != 2 {
		t.Fatalf("valid all-profile transaction failed: count=%d err=%v", count, err)
	}
	if !bytes.Equal(source, overrideReadFixture(t, a.sourceYAMLPath())) || !bytes.Equal(second, overrideReadFixture(t, a.profileContentPath(overrideFixtureID2))) {
		t.Fatal("override save rewrote original YAML")
	}
	saved := overrideReadFixture(t, a.overridesPath())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.saveOverrides(ctx, "global", "", "log-level: debug"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled save accepted")
	}
	if !bytes.Equal(saved, overrideReadFixture(t, a.overridesPath())) {
		t.Fatal("cancelled transaction changed original")
	}
	if _, err := a.saveOverrides(context.Background(), "global", overrideFixtureID1, "{}"); err == nil {
		t.Fatal("global accepted ambiguous profileID")
	}
}
func TestOverridesProfileSaveClearAndSwitch(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, testProfileYAML)
	if count, err := a.saveOverrides(context.Background(), "profile", overrideFixtureID2, "log-level: debug"); err != nil || count != 1 {
		t.Fatal("profile save failed", err)
	}
	if err := a.switchProfile(overrideFixtureID2); err != nil {
		t.Fatal(err)
	}
	_, data, err := a.prepareConfigWithRules([]byte(testProfileYAML), a.previewRuntime(), []CustomRule{})
	if err != nil {
		t.Fatal(err)
	}
	if mappingValue(overrideRenderRoot(t, data), "log-level").Value != "debug" {
		t.Fatal("runtime wrapper did not select active profile override")
	}
	if err := a.switchProfile(overrideFixtureID1); err != nil {
		t.Fatal(err)
	}
	_, data, err = a.prepareConfigWithRules([]byte(testProfileYAML), a.previewRuntime(), []CustomRule{})
	if err != nil {
		t.Fatal(err)
	}
	if mappingValue(overrideRenderRoot(t, data), "log-level") != nil {
		t.Fatal("profile override leaked after switch")
	}
	if _, err := a.saveOverrides(context.Background(), "profile", overrideFixtureID2, "{}"); err != nil {
		t.Fatal(err)
	}
	store, err := a.loadOverrides()
	if err != nil || len(store.Profiles) != 0 {
		t.Fatal("clear did not restore inheritance")
	}
}
func TestOverridesStalePendingCannotMixRuntimeProfileLayers(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, "log-level: debug\n"+testProfileYAML)
	if err := atomicWrite(a.pendingProfilePath(), []byte(overrideFixtureID2), 0o600); err != nil {
		t.Fatal(err)
	}
	if a.sourceYAMLPath() != a.profileContentPath(overrideFixtureID1) {
		t.Fatal("stale pending marker selected another profile")
	}
	if err := a.syncCurrentSource(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(overrideReadFixture(t, filepath.Join(a.dataDir, "source.yaml")), []byte(testProfileYAML)) {
		t.Fatal("runtime source copied from mismatched pending profile")
	}
}

func TestOverridesSubscriptionCandidateUsesTargetID(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, testProfileYAML)
	store := emptyOverrideStore()
	store.Profiles[overrideFixtureID1] = "dns: {listen: 53}"
	store.Profiles[overrideFixtureID2] = "log-level: warning"
	storeOverrideFixture(t, a, store)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer server.Close()
	candidate, err := a.prepareSubscriptionCandidate(context.Background(), Profile{ID: overrideFixtureID2, Source: "subscription"}, server.URL, "direct")
	if err != nil {
		t.Fatal("target update borrowed invalid active profile layer", err)
	}
	if string(candidate.content) != testProfileYAML {
		t.Fatal("subscription original normalized YAML replaced by override render")
	}
	if _, err := a.prepareSubscriptionCandidate(context.Background(), Profile{ID: overrideFixtureID1, Source: "subscription"}, server.URL, "direct"); err == nil {
		t.Fatal("target invalid override was not selected")
	}
}
func TestOverridesImportDoesNotUseActiveLayer(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, "")
	store := emptyOverrideStore()
	store.Profiles[overrideFixtureID1] = "dns: {listen: 53}"
	storeOverrideFixture(t, a, store)
	if err := a.addLocalProfile("Synthetic new local", testProfileYAML); err != nil {
		t.Fatal("new import borrowed active profile layer", err)
	}
}
func TestOverridesRulesUseFinalProfileLayers(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, testProfileYAML)
	store := emptyOverrideStore()
	store.Global = "proxy-groups: [{name: CUSTOM, type: select, proxies: [DIRECT]}]\nrules: ['MATCH,DIRECT']"
	storeOverrideFixture(t, a, store)
	rules := []CustomRule{{ID: "synthetic-rule", Type: "DOMAIN", Value: "example.test", Policy: "CUSTOM", Enabled: true}}
	if err := a.saveCustomRules(context.Background(), rules); err != nil {
		t.Fatal("global override policy omitted from rule validation", err)
	}
	store.Profiles[overrideFixtureID2] = "proxy-groups: [{name: OTHER, type: select, proxies: [DIRECT]}]"
	storeOverrideFixture(t, a, store)
	before := overrideReadFixture(t, a.customRulesPath())
	rules[0].Value = "another.test"
	if err := a.saveCustomRules(context.Background(), rules); err == nil {
		t.Fatal("inactive profile override incompatibility ignored")
	}
	if !bytes.Equal(before, overrideReadFixture(t, a.customRulesPath())) {
		t.Fatal("failed rule validation changed rules")
	}
}

func TestOverridesPreviewReadOnlySecretsAndCandidate(t *testing.T) {
	a := overrideFixtureApp(t, overrideNodeYAML, "")
	store := emptyOverrideStore()
	store.Global = "log-level: warning"
	storeOverrideFixture(t, a, store)
	sentinel := filepath.Join(a.dataDir, "runtime.yaml")
	if err := atomicWrite(sentinel, []byte("SYNTHETIC-RUNTIME-SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{a.overridesPath(), a.profileStorePath(), a.profileContentPath(overrideFixtureID1), a.sourceYAMLPath(), sentinel}
	before := map[string][]byte{}
	for _, p := range paths {
		before[p] = overrideReadFixture(t, p)
	}
	request := overrideHTTPRequest(http.MethodPost, "/api/config/preview", strings.NewReader(`{"profileId":"`+overrideFixtureID1+`","scope":"global","yaml":"log-level: debug"}`))
	response := httptest.NewRecorder()
	a.ConfigPreview(response, request)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("preview request failed", response.Code)
	}
	var result configPreviewResponse
	if json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatal("preview response invalid")
	}
	if !result.Validated || !strings.Contains(result.YAML, "SYNTHETIC-NODE-SECRET") || !strings.Contains(result.YAML, "log-level: debug") {
		t.Fatal("candidate final YAML/native validation missing")
	}
	if strings.Contains(response.Body.String(), a.config.APISecret) || !strings.Contains(result.YAML, overrideSecretPlaceholder) || !strings.Contains(result.Warning, "秘密") || !strings.Contains(result.Warning, "只读") {
		t.Fatal("secret disclosure warning/API redaction missing")
	}
	if !reflect.DeepEqual(result.Precedence, overridePrecedence()) {
		t.Fatal("precedence missing")
	}
	for _, p := range paths {
		if !bytes.Equal(before[p], overrideReadFixture(t, p)) {
			t.Fatal("preview wrote persisted file")
		}
	}
	if a.runtime.Secret != "" || a.operation {
		t.Fatal("preview modified application runtime/operation state")
	}
}
func TestOverridesOfflineValidationDoesNotFetchProvidersOrGeo(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, "")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("proxies: [{name: LOCAL, type: direct}]"))
	}))
	defer server.Close()
	source := "proxy-providers: {SUB: {type: http, path: providers/sub.yaml, url: '" + server.URL + "'}}\nproxy-groups: [{name: TEST, type: select, use: [SUB]}]\nrules: ['MATCH,DIRECT']\n"
	_, rendered, err := a.prepareProfileConfig([]byte(source), "", a.previewRuntime(), []CustomRule{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.testConfigOfflineContext(context.Background(), rendered); err == nil {
		t.Fatal("missing provider cache should fail closed")
	}
	if hits.Load() != 0 {
		t.Fatal("validation fetched missing provider")
	}
	if err := atomicWrite(filepath.Join(a.dataDir, "providers", "sub.yaml"), []byte("proxies: [{name: LOCAL, type: direct}]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.testConfigOfflineContext(context.Background(), rendered); err != nil {
		t.Fatal("cached provider static validation failed", err)
	}
	if hits.Load() != 0 {
		t.Fatal("cached provider was downloaded")
	}
	geo := "proxies: []\ngeox-url: {mmdb: '" + server.URL + "'}\nrules: [GEOIP,CN,DIRECT, MATCH,DIRECT]"
	// Quotes preserve rule entries containing commas.
	geo = "proxies: []\ngeox-url: {mmdb: '" + server.URL + "'}\nrules: ['GEOIP,CN,DIRECT', 'MATCH,DIRECT']"
	_, rendered, err = a.prepareProfileConfig([]byte(geo), "", a.previewRuntime(), []CustomRule{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.testConfigOfflineContext(context.Background(), rendered); err == nil {
		t.Fatal("missing Geo resource should fail closed")
	}
	if hits.Load() != 0 {
		t.Fatal("validation downloaded Geo")
	}
}
func TestOverridesPreviewUnavailableIsExplicit(t *testing.T) {
	a := overrideFixtureApp(t, "proxies: []\nrules: ['GEOIP,CN,DIRECT', 'MATCH,DIRECT']\n", "")
	response := httptest.NewRecorder()
	a.ConfigPreview(response, overrideHTTPRequest(http.MethodPost, "/api/config/preview", strings.NewReader(`{"profileId":"`+overrideFixtureID1+`"}`)))
	var result configPreviewResponse
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatal("read-only unvalidated preview missing")
	}
	if result.Validated || result.YAML == "" || !strings.Contains(result.Validation, "不会联网") {
		t.Fatal("unavailable validation falsely marked validated")
	}
}
func TestOverridesRoutesRejectUntrustedPreviewAndMutations(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, "")
	handler := a.Routes()
	for _, path := range []string{"/api/overrides", "/api/config/preview"} {
		for _, origin := range []string{"missing-marker", "https://example.invalid"} {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17653"+path, strings.NewReader("{}"))
			if origin != "missing-marker" {
				r.Header.Set("X-Mihomo-Lite", "1")
				r.Header.Set("Origin", origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, r)
			if response.Code != http.StatusForbidden || a.operation {
				t.Fatal("untrusted request reached mutation/preview handler")
			}
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, overrideHTTPRequest(http.MethodGet, "http://127.0.0.1:17653/api/overrides?scope=global", nil))
	if response.Code != http.StatusOK {
		t.Fatal("saved override GET route unavailable")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, overrideHTTPRequest(http.MethodPost, "http://127.0.0.1:17653/api/config/preview", strings.NewReader(`{"profileId":"`+overrideFixtureID1+`"}`)))
	if response.Code != http.StatusOK || a.operation || fileExists(a.overridesPath()) {
		t.Fatal("saved preview acquired write operation or persisted overrides")
	}
}

func TestOverridesHTTPContractAndMalformedRequests(t *testing.T) {
	a := overrideFixtureApp(t, testProfileYAML, "")
	response := httptest.NewRecorder()
	a.Overrides(response, overrideHTTPRequest(http.MethodGet, "/api/overrides?scope=profile&profileId="+overrideFixtureID1, nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "precedence") {
		t.Fatal("GET contract failed")
	}
	for _, body := range []string{`{"scope":"profile","profileId":"../bad","yaml":"{}"}`, `{"scope":"global"}`, `{"scope":"global","yaml":null}`, `{"scope":"global","yaml":"dns: null"}`, `{"scope":"global","yaml":"{}","unexpected":true}`, `{"scope":"global","scope":"profile","yaml":"{}"}`, `{"Scope":"global","yaml":"{}"}`, `{"scope":"global","profileId":null,"yaml":"{}"}`} {
		response := httptest.NewRecorder()
		a.Overrides(response, overrideHTTPRequest(http.MethodPost, "/api/overrides", strings.NewReader(body)))
		if response.Code != 400 {
			t.Fatal("malformed override request accepted")
		}
	}
	response = httptest.NewRecorder()
	a.Overrides(response, overrideHTTPRequest(http.MethodPost, "/api/overrides", strings.NewReader(`{"scope":"global","yaml":"log-level: warning"}`)))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"applied":false`) || !strings.Contains(response.Body.String(), `"validatedProfiles":1`) {
		t.Fatal("save response contract failed")
	}
	response = httptest.NewRecorder()
	a.Overrides(response, overrideHTTPRequest(http.MethodDelete, "/api/overrides", nil))
	if response.Code != 405 {
		t.Fatal("unsupported method accepted")
	}
}
