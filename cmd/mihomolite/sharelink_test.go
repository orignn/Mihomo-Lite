package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const opt009UUID = "00000000-0000-4000-8000-000000000001"
const opt009Password = "SYNTHETIC-NOT-A-REAL-SECRET"

func opt009VMess(extra map[string]any) string {
	value := map[string]any{"v": "2", "ps": "合成 VMess", "add": "127.0.0.1", "port": "9", "id": opt009UUID, "aid": "0", "scy": "auto", "net": "tcp", "type": "none", "host": "", "path": "", "tls": ""}
	for key, item := range extra {
		value[key] = item
	}
	raw, _ := json.Marshal(value)
	return "vmess://" + base64.RawStdEncoding.EncodeToString(raw)
}

type opt009Case struct{ name, link, protocol string }

func opt009ValidCases() []opt009Case {
	vless := "vless://" + opt009UUID + "@127.0.0.1:9"
	trojan := "trojan://" + opt009Password + "@127.0.0.1:9"
	hy2 := "hy2://" + opt009Password + "@127.0.0.1:9"
	tuic := "tuic://" + opt009UUID + ":" + opt009Password + "@127.0.0.1:9"
	ss := "ss://aes-128-gcm:" + opt009Password + "@127.0.0.1:9"
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	return []opt009Case{
		{"ss-plain", ss, "ss"},
		{"ss-password-punctuation", "ss://aes-128-gcm:" + url.PathEscape("SYN:@/+%:#") + "@[::1]:9#%E5%90%88%E6%88%90%20%2B", "ss"},
		{"ss-sip002", "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:"+opt009Password)) + "@127.0.0.1:9/#SS", "ss"},
		{"ss-legacy", "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:"+opt009Password+"@127.0.0.1:9")) + "#SS", "ss"},
		{"ss-2022", "ss://2022-blake3-aes-128-gcm:" + url.PathEscape(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))) + "@127.0.0.1:9", "ss"},
		{"ss-obfs-http", ss + "?plugin=" + url.QueryEscape("obfs-local;obfs=http;obfs-host=example.test"), "ss"},
		{"ss-obfs-tls", ss + "?plugin=" + url.QueryEscape("simple-obfs;obfs=tls;obfs-host=example.test"), "ss"},
		{"ss-v2ray-ws", ss + "?plugin=" + url.QueryEscape("v2ray-plugin;mode=websocket;host=example.test;path=/ws"), "ss"},
		{"ss-v2ray-wss", ss + "?plugin=" + url.QueryEscape("v2ray-plugin;mode=websocket;tls;host=example.test;path=/ws;skip-cert-verify"), "ss"},
		{"vmess-json-tcp", opt009VMess(nil), "vmess"},
		{"vmess-json-wss", opt009VMess(map[string]any{"port": 9, "aid": 0, "net": "ws", "host": "example.test", "path": "/ws?ed=2048", "tls": "tls", "sni": "example.test", "alpn": "h2,http/1.1", "fp": "chrome", "allowInsecure": true}), "vmess"},
		{"vmess-json-grpc", opt009VMess(map[string]any{"net": "grpc", "path": "svc", "tls": "tls", "sni": "example.test"}), "vmess"},
		{"vmess-uri", "vmess://" + opt009UUID + "@[::1]:9?encryption=aes-128-gcm&aid=0&security=tls&type=ws&path=%2Fws&host=example.test&sni=example.test", "vmess"},
		{"vless-tcp", vless + "?encryption=none&security=none&type=tcp", "vless"},
		{"vless-wss", vless + "?security=tls&type=ws&path=%2Fws&host=example.test&sni=example.test&fp=chrome&allowInsecure=1&alpn=h2,http%2F1.1", "vless"},
		{"vless-grpc", vless + "?security=tls&type=grpc&serviceName=svc&mode=gun", "vless"},
		{"vless-reality-vision", vless + "?security=reality&pbk=" + key + "&sid=abcd&sni=example.test&flow=xtls-rprx-vision", "vless"},
		{"vless-ws-early-data", vless + "?type=ws&path=%2Fws%3Fed%3D2048", "vless"},
		{"vless-ipv6", "VLESS://" + opt009UUID + "@[2001:db8::1]:443?security=tls#%E5%90%88%E6%88%90%20%2B", "vless"},
		{"trojan-default-tls", trojan, "trojan"},
		{"trojan-default-port", "trojan://" + opt009Password + "@127.0.0.1", "trojan"},
		{"trojan-wss", trojan + "?type=ws&path=%2Fws&peer=example.test&allowInsecure=true", "trojan"},
		{"trojan-grpc", trojan + "?type=grpc&serviceName=svc&sni=example.test&alpn=h2", "trojan"},
		{"hy2-default", hy2, "hysteria2"},
		{"hysteria2-ipv6-auth", "hysteria2://user:SYN%3A%40%2B@[::1]:9/?sni=example.test&insecure=1&alpn=h3", "hysteria2"},
		{"hy2-obfs-bandwidth", hy2 + "?obfs=salamander&obfs-password=SYN&upmbps=20&down=100", "hysteria2"},
		{"hy2-port-hopping", hy2 + "?mport=9,100-102&hopInterval=30", "hysteria2"},
		{"tuic-v5", tuic, "tuic"},
		{"tuic-quic-options", tuic + "?sni=example.test&alpn=h3&congestion_control=bbr&udp_relay_mode=quic&reduce_rtt=1&allowInsecure=0", "tuic"},
		{"tuic-disable-sni", tuic + "?disable_sni=true&congestion-control=cubic&udp-relay-mode=native", "tuic"},
	}
}
func TestOPT009Protocols(t *testing.T) {
	for _, tc := range opt009ValidCases() {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseShareLink(tc.link)
			if err != nil {
				t.Fatalf("synthetic valid fixture rejected: %v", err)
			}
			if p.Type != tc.protocol || p.Name == "" || p.Server == "" || p.Port == 0 || !p.UDP {
				t.Fatal("node identity or protocol lost")
			}
			data, err := convertShareLinkSubscription([]byte(tc.link), tc.protocol)
			if err != nil {
				t.Fatalf("conversion failed: %v", err)
			}
			var profile shareProfile
			if yaml.Unmarshal(data, &profile) != nil || len(profile.Proxies) != 1 || !reflect.DeepEqual(p, profile.Proxies[0]) {
				t.Fatal("YAML round trip changed node settings")
			}
		})
	}
}
func TestOPT009ParameterProjection(t *testing.T) {
	cases := opt009ValidCases()
	check := func(name string) shareProxy {
		t.Helper()
		for _, tc := range cases {
			if tc.name == name {
				p, err := parseShareLink(tc.link)
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
		}
		t.Fatal("missing synthetic case")
		return shareProxy{}
	}
	p := check("ss-password-punctuation")
	if p.Password != "SYN:@/+%:#" || p.Server != "::1" || p.Name != "合成 +" {
		t.Fatal("URL credentials, IPv6, or Unicode name changed")
	}
	p = check("vmess-json-wss")
	if p.Cipher != "auto" || !p.TLS || !p.SkipCertVerify || p.Servername != "example.test" || p.ClientFingerprint != "chrome" || p.WSOpts.Path != "/ws" || p.WSOpts.MaxEarlyData != 2048 || p.WSOpts.EarlyDataHeaderName != "Sec-WebSocket-Protocol" || p.WSOpts.Headers["Host"] != "example.test" || len(p.ALPN) != 2 {
		t.Fatal("VMess TLS/WS settings lost")
	}
	p = check("vless-reality-vision")
	if p.RealityOpts == nil || p.RealityOpts.ShortID != "abcd" || p.Flow != "xtls-rprx-vision" || p.ClientFingerprint != "chrome" {
		t.Fatal("Reality settings lost")
	}
	p = check("trojan-grpc")
	if !p.TLS || p.SNI != "example.test" || p.Servername != "" || p.GrpcOpts.GrpcServiceName != "svc" {
		t.Fatal("Trojan gRPC/TLS settings lost")
	}
	p = check("hysteria2-ipv6-auth")
	if p.Password != "user:SYN:@+" || p.SNI != "example.test" || !p.SkipCertVerify || p.TLS {
		t.Fatal("Hysteria2 auth/TLS settings lost")
	}
	p = check("tuic-quic-options")
	if p.CongestionController != "bbr" || p.UDPRelayMode != "quic" || !p.ReduceRTT || p.SNI != "example.test" {
		t.Fatal("TUIC options lost")
	}
}
func TestOPT009Base64AndURLVariants(t *testing.T) {
	codecs := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	for i, codec := range codecs {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			for _, legacy := range []bool{false, true} {
				text := "aes-128-gcm:SYN:@/+%:#"
				suffix := "@[::1]:9#合成"
				if legacy {
					text += "@[::1]:9"
					suffix = "#合成"
				}
				link := "ss://" + url.PathEscape(codec.EncodeToString([]byte(text))) + suffix
				p, err := parseShareLink(link)
				if err != nil || p.Password != "SYN:@/+%:#" || p.Server != "::1" {
					t.Fatal("SS Base64 variant failed")
				}
			}
			original := opt009VMess(map[string]any{"ps": "合成😀", "port": 9, "aid": 0})
			raw, _ := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(original, "vmess://"))
			for _, body := range []string{codec.EncodeToString(raw), url.PathEscape(codec.EncodeToString(raw)), url.PathEscape(string(raw))} {
				p, err := parseShareLink("vmess://" + body)
				if err != nil || p.Name != "合成😀" {
					t.Fatal("VMess payload variant failed")
				}
			}
			links := make([]string, 0)
			for _, tc := range opt009ValidCases() {
				links = append(links, tc.link)
			}
			plain := strings.Join(links, "\r\n")
			encoded := codec.EncodeToString([]byte(plain))
			wrapped := encoded[:40] + "\r\n" + encoded[40:]
			for _, input := range []string{encoded, wrapped, url.PathEscape(encoded), "\ufeff" + plain, plain} {
				scheme, found := detectShareLinkSubscription([]byte(input))
				if !found || scheme != "ss" {
					t.Fatal("mixed subscription detection failed")
				}
				output, err := (&Application{}).normalizeProfileInput([]byte(input), nil)
				if err != nil {
					t.Fatalf("mixed conversion failed: %v", err)
				}
				var profile shareProfile
				if yaml.Unmarshal(output, &profile) != nil || len(profile.Proxies) != len(links) {
					t.Fatal("mixed subscription dropped nodes")
				}
			}
		})
	}
	// Unescaped + and / in standard Base64 must not become URL delimiters.
	unicodePassword := "SYN:\uffff"
	for _, codec := range codecs {
		encoded := codec.EncodeToString([]byte("aes-128-gcm:" + unicodePassword))
		p, err := parseShareLink("ss://" + encoded + "@[::1]:9")
		if err != nil || p.Password != unicodePassword || p.Server != "::1" {
			t.Fatal("unescaped SS Base64 alphabet corrupted credentials")
		}
	}
	for _, tc := range opt009ValidCases() {
		if shareOneOf(tc.protocol, "ss", "vmess", "vless", "trojan") && !strings.HasPrefix(tc.name, "vmess-json") {
			link := tc.link
			fragment := ""
			if idx := strings.IndexByte(link, '#'); idx >= 0 {
				fragment, link = link[idx:], link[:idx]
			}
			sep := "?"
			if strings.Contains(link, "?") {
				sep = "&"
			}
			p, err := parseShareLink(link + sep + "udp=0" + fragment)
			if err != nil || p.UDP {
				t.Fatal("explicit UDP disable was ignored")
			}
		}
	}
	short := "ss://YWVzLTEyOC1nY206Uw@a:9"
	if _, found := detectShareLinkSubscription([]byte(base64.RawURLEncoding.EncodeToString([]byte(short)))); !found {
		t.Fatal("short Base64 misclassified")
	}
	if _, found := detectShareLinkSubscription([]byte("metadata: ss://not-a-subscription")); found {
		t.Fatal("YAML metadata misclassified")
	}
}
func TestOPT009InvalidAndUnsupportedAreAtomicAndRedacted(t *testing.T) {
	vless := "vless://" + opt009UUID + "@127.0.0.1:9"
	ss := "ss://aes-128-gcm:" + opt009Password + "@127.0.0.1:9"
	hy2 := "hy2://" + opt009Password + "@127.0.0.1:9"
	tuic := "tuic://" + opt009UUID + ":" + opt009Password + "@127.0.0.1:9"
	cases := []struct{ name, link string }{
		{"missing-user", "vless://127.0.0.1:9"}, {"invalid-uuid", "vless://" + opt009Password + "@127.0.0.1:9"},
		{"missing-host", "vless://" + opt009UUID + "@:9"}, {"missing-port", "vless://" + opt009UUID + "@127.0.0.1"},
		{"port-zero", strings.Replace(vless, ":9", ":0", 1)}, {"port-overflow", strings.Replace(vless, ":9", ":65536", 1)},
		{"port-text", strings.Replace(vless, ":9", ":"+opt009Password, 1)}, {"ipv6-brackets", "vless://" + opt009UUID + "@2001:db8::1:9"},
		{"url-percent", vless + "#%XX"}, {"name-control", vless + "#%0A"}, {"sni-control", vless + "?security=tls&sni=%0A"},
		{"credential-component", "vless://" + opt009UUID + ":SYN@127.0.0.1:9"}, {"unknown-param", vless + "?token=" + opt009Password},
		{"unknown-param-empty", vless + "?unsupported="}, {"duplicate-security", vless + "?security=tls&security=none"},
		{"conflicting-sni", vless + "?security=tls&sni=a&peer=b"}, {"invalid-boolean", vless + "?security=tls&allowInsecure=" + opt009Password},
		{"malformed-query", vless + "?security=tls&token=%XX"}, {"semicolon-query", vless + "?security=tls;sni=a"},
		{"unsupported-encryption", vless + "?encryption=aes-128-gcm"}, {"unsupported-flow", vless + "?security=tls&flow=xtls-rprx-direct"},
		{"flow-without-tls", vless + "?flow=xtls-rprx-vision"}, {"flow-ws", vless + "?security=tls&type=ws&flow=xtls-rprx-vision"},
		{"unknown-fingerprint", vless + "?security=tls&fp=" + opt009Password}, {"tls-param-with-none", vless + "?sni=example.test"},
		{"h2-transport", vless + "?security=tls&type=h2"}, {"xhttp-transport", vless + "?type=xhttp"}, {"tcp-http-header", vless + "?headerType=http"},
		{"tcp-path", vless + "?path=/ws"}, {"grpc-multi", vless + "?type=grpc&mode=multi"}, {"grpc-host", vless + "?type=grpc&host=a"},
		{"ws-early-conflict", vless + "?type=ws&ed=2&path=%2Fws%3Fed%3D3"}, {"ws-early-mixed", vless + "?type=ws&path=%2Fws%3Fed%3D3%26x%3D1"},
		{"reality-no-key", vless + "?security=reality"}, {"reality-invalid-key", vless + "?security=reality&pbk=" + opt009Password},
		{"ss-bad-b64", "ss://" + opt009Password}, {"ss-no-password", "ss://aes-128-gcm:@127.0.0.1:9"},
		{"ss-invalid-cipher", strings.Replace(ss, "aes-128-gcm", opt009Password, 1)}, {"ss-invalid-2022", strings.Replace(ss, "aes-128-gcm", "2022-blake3-aes-128-gcm", 1)},
		{"ss-unsupported-plugin", ss + "?plugin=" + url.QueryEscape("unknown;secret="+opt009Password)},
		{"ss-plugin-unknown", ss + "?plugin=" + url.QueryEscape("v2ray-plugin;unknown="+opt009Password)},
		{"ss-plugin-quic", ss + "?plugin=" + url.QueryEscape("v2ray-plugin;mode=quic")}, {"ss-plugin-mux", ss + "?plugin=" + url.QueryEscape("v2ray-plugin;mux")},
		{"ss-obfs-path", ss + "?plugin=" + url.QueryEscape("simple-obfs;obfs=http;obfs-uri=/ws")},
		{"trojan-no-tls", "trojan://SYN@127.0.0.1:9?security=none"}, {"trojan-user-colon", "trojan://user:SYN@127.0.0.1:9"},
		{"hy1", "hysteria://" + opt009Password + "@127.0.0.1:9"}, {"hy2-unknown-obfs", hy2 + "?obfs=unknown&obfs-password=SYN"},
		{"hy2-missing-obfs-secret", hy2 + "?obfs=salamander"}, {"hy2-secret-no-obfs", hy2 + "?obfs-password=SYN"},
		{"hy2-negative-up", hy2 + "?up=-1"}, {"hy2-bad-ports", hy2 + "?mport=20-9"}, {"hy2-port-zero", hy2 + "?mport=0"},
		{"hy2-no-ports-hop", hy2 + "?hopInterval=30"}, {"hy2-fingerprint", hy2 + "?fp=chrome"}, {"hy2-pin", hy2 + "?pinSHA256=" + opt009Password},
		{"tuic-v4", "tuic://" + opt009Password + "@127.0.0.1:9"}, {"tuic-no-password", "tuic://" + opt009UUID + "@127.0.0.1:9"},
		{"tuic-bad-congestion", tuic + "?congestion_control=" + opt009Password}, {"tuic-bad-relay", tuic + "?udp_relay_mode=other"},
		{"tuic-disable-conflict", tuic + "?disable_sni=true&sni=example.test"}, {"tuic-heartbeat", tuic + "?heartbeat=10"},
		{"json-unknown", opt009VMess(map[string]any{"unknown": opt009Password})}, {"json-bad-aid", opt009VMess(map[string]any{"aid": -1})},
		{"json-header-http", opt009VMess(map[string]any{"type": "http"})}, {"json-quic", opt009VMess(map[string]any{"net": "quic"})},
		{"json-tcp-host", opt009VMess(map[string]any{"host": "example.test"})}, {"json-duplicate", "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(`{"id":"SYN","id":"SYN"}`))},
		{"json-nested", opt009VMess(map[string]any{"tls": map[string]string{"secret": opt009Password}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseShareLink(tc.link); err == nil {
				t.Fatal("invalid node accepted")
			}
			input := []byte(vless + "#GOOD\n" + tc.link + "\n" + ss)
			original := append([]byte(nil), input...)
			output, err := convertShareLinkSubscription(input, "vless")
			if err == nil || output != nil || !bytes.Equal(input, original) {
				t.Fatal("invalid mixed subscription was partially converted or mutated")
			}
			var safe *shareLinkConversionError
			if !errors.As(err, &safe) || !strings.Contains(err.Error(), "第 2 条") || !strings.Contains(err.Error(), "未导入任何节点") {
				t.Fatal("missing safe atomic conversion diagnostic")
			}
			if strings.Contains(err.Error(), opt009Password) || strings.Contains(err.Error(), opt009UUID) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "GOOD") {
				t.Fatal("conversion error leaked input")
			}
		})
	}
}
func TestOPT009NamingAndNoCredentialDedup(t *testing.T) {
	link := "ss://aes-128-gcm:SYN@127.0.0.1:9"
	names := []string{"重复", "重复", "重复 (2)", "DIRECT", "节点选择", unifiedProxyName, "# YAML: [x]"}
	var links []string
	for i, name := range names {
		links = append(links, strings.Replace(link, ":SYN@", fmt.Sprintf(":SYN%d@", i), 1)+"#"+url.PathEscape(name))
	}
	links = append(links, links[0])
	output, err := convertShareLinkSubscription([]byte(strings.Join(links, "\n")), "ss")
	if err != nil {
		t.Fatal(err)
	}
	var profile shareProfile
	if yaml.Unmarshal(output, &profile) != nil || len(profile.Proxies) != len(links) {
		t.Fatal("same endpoint with different credentials was dropped")
	}
	seen := map[string]bool{"DIRECT": true, "节点选择": true, unifiedProxyName: true}
	for _, p := range profile.Proxies {
		if seen[p.Name] {
			t.Fatal("duplicate/reserved node name")
		}
		seen[p.Name] = true
	}
	if len(profile.ProxyGroups[0].Proxies) != len(links)+1 {
		t.Fatal("group lost a node")
	}
}
func TestOPT009BoundsAndSafeErrorType(t *testing.T) {
	for _, input := range [][]byte{nil, []byte("%%%"), bytes.Repeat([]byte{'a'}, 4<<20+1), []byte("\xff"), []byte(strings.Repeat("ss://SYN\n", 10001))} {
		output, err := convertShareLinkSubscription(input, "secret-scheme")
		if err == nil || output != nil || safeShareLinkConversionError(err) == nil || strings.Contains(err.Error(), "secret-scheme") {
			t.Fatal("invalid payload lacks safe bound failure")
		}
	}
	if safeShareLinkConversionError(errors.New(opt009Password)) != nil {
		t.Fatal("unsafe error allowed through")
	}
	if _, err := parseShareLink("ss://" + strings.Repeat("a", 65536)); err == nil {
		t.Fatal("oversized node accepted")
	}
}
func opt009Snapshot(t *testing.T, base string) map[string][]byte {
	t.Helper()
	result := map[string][]byte{}
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, _ := filepath.Rel(base, path)
			result[relative] = raw
		}
		return nil
	})
	if err != nil {
		t.Fatal("fixture snapshot failed")
	}
	return result
}
func TestOPT009ImportUpdateFailuresPreserveAllFiles(t *testing.T) {
	// No core binary is even installed. A parse failure must return before any
	// validator, persistence, reconnect, or actual system-proxy operation.
	a := testApplication(t)
	a.config = &Settings{}
	var logs bytes.Buffer
	a.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	id := strings.Repeat("a", 64)
	invalid := "ss://aes-128-gcm:" + opt009Password + "@127.0.0.1:9\nvless://" + opt009UUID + "@127.0.0.1:9?type=xhttp#" + opt009Password
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(invalid)) }))
	defer server.Close()
	store := profileStore{ActiveID: id, Profiles: []Profile{{ID: id, Name: "SYN", Source: "subscription", URL: server.URL}}}
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{a.profileContentPath(id), a.profileContentPath(id) + ".bak", filepath.Join(a.dataDir, "source.yaml"), a.customRulesPath(), a.pendingProfilePath(), a.subscriptionDiffPath(id), filepath.Join(a.baseDir, "launcher.json")} {
		content := []byte(testProfileYAML)
		if path == a.customRulesPath() {
			content = []byte("[]")
		}
		if err := atomicWrite(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := opt009Snapshot(t, a.baseDir)
	actions := []struct {
		name string
		run  func() error
	}{
		{"paste", func() error { return a.addLocalProfile("SYN", invalid) }},
		{"subscription-import", func() error { return a.addSubscription(context.Background(), "SYN", server.URL, "direct") }},
		{"subscription-candidate", func() error {
			_, err := a.prepareSubscriptionCandidate(context.Background(), store.Profiles[0], server.URL, "direct")
			return err
		}},
		{"normalization-template", func() error { _, err := a.normalizeProfileInput([]byte(invalid), []byte(testProfileYAML)); return err }},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			err := action.run()
			if err == nil {
				t.Fatal("bad links accepted")
			}
			if strings.Contains(err.Error(), opt009Password) || strings.Contains(err.Error(), opt009UUID) {
				t.Fatal("entrypoint leaked node data")
			}
			if safeShareLinkConversionError(err) == nil || !strings.Contains(err.Error(), "第 2 条") {
				t.Fatal("entrypoint lost safe conversion diagnostic")
			}
			if !reflect.DeepEqual(before, opt009Snapshot(t, a.baseDir)) {
				t.Fatal("conversion failure changed existing fixture files")
			}
			if strings.Contains(logs.String(), opt009Password) || strings.Contains(logs.String(), opt009UUID) {
				t.Fatal("entrypoint logs leaked node data")
			}
		})
	}
	t.Run("actual-update-failure-record-only", func(t *testing.T) {
		err := a.updateSubscription(context.Background(), id, "direct")
		if err == nil || safeShareLinkConversionError(err) == nil || !strings.Contains(err.Error(), "第 2 条") {
			t.Fatal("actual update lost safe node diagnostic")
		}
		after := opt009Snapshot(t, a.baseDir)
		storePath, _ := filepath.Rel(a.baseDir, a.profileStorePath())
		delete(after, storePath)
		beforeFiles := make(map[string][]byte)
		for path, data := range before {
			if path != storePath {
				beforeFiles[path] = data
			}
		}
		if !reflect.DeepEqual(beforeFiles, after) {
			t.Fatal("failed update changed configuration, backup, rules, diff or pending identity")
		}
		current, err := a.loadProfileStore()
		if err != nil || current.ActiveID != id || len(current.Profiles) != 1 || current.Profiles[0].ID != id || current.Profiles[0].URL != server.URL || current.Profiles[0].LastUpdate == nil || current.Profiles[0].LastUpdate.Status != "failed" {
			t.Fatal("failed update changed profile identity or lost failure metadata")
		}
		if strings.Contains(logs.String(), opt009Password) || strings.Contains(logs.String(), opt009UUID) {
			t.Fatal("failed update logs leaked node data")
		}
	})
}
