package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// All errors below are fixed messages. In particular, never return url.Parse,
// ParseQuery, JSON, Base64, or numeric errors: they can expose user credentials.
var errShareParams = errors.New("参数无效、冲突或尚不支持；请使用完整 mihomo YAML")
var errShareURL = errors.New("链接结构、服务器、端口或凭据无效")

// Only this typed error is safe to forward through subscription/UI boundaries.
type shareLinkConversionError struct{ message string }

func (e *shareLinkConversionError) Error() string { return e.message }
func safeShareLinkConversionError(err error) error {
	var safe *shareLinkConversionError
	if errors.As(err, &safe) {
		return safe
	}
	return nil
}

func shareScheme(text string) string {
	i := strings.Index(text, "://")
	if i <= 0 || i > 20 {
		return ""
	}
	for _, r := range text[:i] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '-' && r != '.' {
			return ""
		}
	}
	return strings.ToLower(text[:i])
}
func cleanShareText(text string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "\ufeff"))
}
func shareBase64(text string) ([]byte, error) {
	text, err := url.PathUnescape(text)
	if err != nil {
		return nil, errShareURL
	}
	text = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, text)
	for _, codec := range []*base64.Encoding{base64.StdEncoding.Strict(), base64.RawStdEncoding.Strict(), base64.URLEncoding.Strict(), base64.RawURLEncoding.Strict()} {
		if decoded, err := codec.DecodeString(text); err == nil && utf8.Valid(decoded) {
			return decoded, nil
		}
	}
	return nil, errors.New("Base64 编码无效")
}
func shareText(content []byte) (string, error) {
	if len(content) > 4<<20 || !utf8.Valid(content) {
		return "", errors.New("分享链接为空、编码无效或超过 4 MiB")
	}
	text := cleanShareText(string(content))
	if shareScheme(text) != "" {
		return text, nil
	}
	if decoded, err := url.PathUnescape(text); err == nil && shareScheme(cleanShareText(decoded)) != "" {
		return cleanShareText(decoded), nil
	}
	decoded, err := shareBase64(text)
	if err != nil {
		return "", errors.New("分享链接订阅不是有效的文本或 Base64")
	}
	return cleanShareText(string(decoded)), nil
}
func decodeShareLinks(content []byte) ([]string, error) {
	text, err := shareText(content)
	if err != nil {
		return nil, err
	}
	var links []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			links = append(links, line)
		}
	}
	if len(links) > 10000 {
		return nil, errors.New("分享链接节点数量超过 10000")
	}
	return links, nil
}

// Taking a parameter consumes all its aliases, requires one unambiguous value,
// and leaves unknown parameters to finish(). This prevents wrong connections
// from silently discarded transports/security/plugin knobs.
type shareParams struct {
	values url.Values
	used   map[string]bool
	err    error
}

func newShareParams(raw string) *shareParams {
	values, err := url.ParseQuery(raw)
	q := &shareParams{values: values, used: map[string]bool{}}
	if err != nil {
		q.err = errShareParams
	}
	return q
}
func (q *shareParams) take(keys ...string) string {
	value, found := "", false
	for _, key := range keys {
		q.used[key] = true
		if values, exists := q.values[key]; exists {
			if len(values) != 1 {
				q.err = errShareParams
				continue
			}
			if found && value != values[0] {
				q.err = errShareParams
			}
			value, found = values[0], true
		}
	}
	return value
}
func (q *shareParams) boolean(keys ...string) bool {
	value := q.take(keys...)
	if value == "" {
		return false
	}
	switch strings.ToLower(value) {
	case "1", "true":
		return true
	case "0", "false":
		return false
	}
	q.err = errShareParams
	return false
}
func (q *shareParams) integer(min, max int, keys ...string) int {
	value := q.take(keys...)
	if value == "" {
		return 0
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < min || number > max {
		q.err = errShareParams
	}
	return number
}
func (q *shareParams) finish() error {
	if q.err != nil {
		return q.err
	}
	for key, values := range q.values {
		for _, value := range values {
			if !validShareString(value) {
				return errShareParams
			}
		}
		if !q.used[key] {
			return errShareParams
		}
	}
	return nil
}
func validShareString(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validShareUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}
func setShareEndpoint(p *shareProxy, host, port string, defaultPort int) error {
	if host == "" || !validShareString(host) || strings.ContainsAny(host, " /?#@\\") {
		return errShareURL
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return errShareURL
	}
	number := defaultPort
	if port != "" {
		var err error
		number, err = strconv.Atoi(port)
		if err != nil {
			return errShareURL
		}
	}
	if number < 1 || number > 65535 {
		return errShareURL
	}
	p.Server, p.Port, p.UDP = host, number, true
	return nil
}
func parseShareURI(link string, defaultPort int) (shareProxy, *url.URL, *shareParams, error) {
	p := shareProxy{}
	u, err := url.Parse(link)
	if err != nil || u.Opaque != "" || u.User == nil || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return p, nil, nil, errShareURL
	}
	if err := setShareEndpoint(&p, u.Hostname(), u.Port(), defaultPort); err != nil {
		return p, nil, nil, err
	}
	// Require brackets for IPv6, rather than interpreting its last component as a port.
	if strings.Count(u.Host, ":") > 1 && !strings.HasPrefix(u.Host, "[") {
		return p, nil, nil, errShareURL
	}
	q := newShareParams(u.RawQuery)
	if _, exists := q.values["udp"]; exists && shareOneOf(strings.ToLower(u.Scheme), "ss", "vmess", "vless", "trojan") {
		p.UDP = q.boolean("udp")
	}
	name := q.take("remarks", "name")
	if u.Fragment != "" {
		if name != "" && name != u.Fragment {
			q.err = errShareParams
		}
		name = u.Fragment
	}
	if !validShareString(name) {
		return p, nil, nil, errShareParams
	}
	p.Name = name
	if p.Name == "" {
		p.Name = net.JoinHostPort(p.Server, strconv.Itoa(p.Port))
	}
	return p, u, q, nil
}
func parseShareLink(link string) (shareProxy, error) {
	if len(link) > 64<<10 || !validShareString(link) {
		return shareProxy{}, errShareURL
	}
	switch scheme := shareScheme(link); scheme {
	case "ss":
		return parseSSLink(link)
	case "vmess":
		return parseVMessLink(link)
	case "vless":
		return parseVlessLink(link)
	case "trojan":
		return parseTrojanLink(link)
	case "hysteria2", "hy2":
		return parseHysteria2Link(link)
	case "tuic":
		return parseTUICLink(link)
	default:
		return shareProxy{}, errors.New("协议尚不支持或不是分享链接；支持 SS、VMess、VLESS、Trojan、Hysteria2/hy2、TUIC")
	}
}

func applyShareTLS(p *shareProxy, q *shareParams, security string) error {
	if security != "none" && security != "tls" && security != "reality" {
		return errShareParams
	}
	sni := q.take("sni", "peer", "servername")
	fp := q.take("fp", "fingerprint", "client-fingerprint")
	skip := q.boolean("allowInsecure", "insecure", "skip-cert-verify")
	alpn := q.take("alpn")
	if security == "none" {
		if sni != "" || fp != "" || skip || alpn != "" {
			return errShareParams
		}
		return nil
	}
	p.TLS, p.SkipCertVerify = true, skip
	if sni == "" {
		sni = p.Server
	}
	if !validShareString(sni) || !validShareString(fp) {
		return errShareParams
	}
	// Mihomo falls back silently for unrecognized uTLS fingerprints.
	if fp != "" && !shareOneOf(fp, "chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized") {
		return errShareParams
	}
	p.Servername, p.ClientFingerprint = sni, fp
	if alpn != "" {
		for _, value := range strings.Split(alpn, ",") {
			if value == "" || len(value) > 255 || !validShareString(value) {
				return errShareParams
			}
			p.ALPN = append(p.ALPN, value)
		}
	}
	if security == "reality" {
		if p.Type != "vless" || skip {
			return errShareParams
		}
		publicKey, shortID := q.take("pbk", "public-key"), q.take("sid", "short-id")
		key, err := base64.RawURLEncoding.Strict().DecodeString(publicKey)
		if err != nil || len(key) != 32 || len(shortID) > 16 || len(shortID)%2 != 0 {
			return errShareParams
		}
		if _, err := hex.DecodeString(shortID); err != nil {
			return errShareParams
		}
		p.RealityOpts = &realityOpts{PublicKey: publicKey, ShortID: shortID}
		if p.ClientFingerprint == "" {
			p.ClientFingerprint = "chrome"
		}
	}
	return nil
}
func shareOneOf(value string, values ...string) bool {
	for _, item := range values {
		if value == item {
			return true
		}
	}
	return false
}
func applyShareTransport(p *shareProxy, q *shareParams) error {
	network := q.take("type", "network", "net")
	if network == "" {
		network = "tcp"
	}
	p.Network = network
	switch network {
	case "tcp":
		header := q.take("headerType")
		if header != "" && header != "none" {
			return errShareParams
		}
	case "ws":
		path, host := q.take("path"), q.take("host")
		if !validShareString(path) || !validShareString(host) {
			return errShareParams
		}
		// Xray embeds early data as ?ed=N in the WS path. Translate it instead
		// of forwarding a different HTTP request target to mihomo.
		ed := q.integer(0, 65535, "ed", "max-early-data")
		if idx := strings.IndexByte(path, '?'); idx >= 0 {
			nested := newShareParams(path[idx+1:])
			if _, exists := nested.values["ed"]; exists {
				nestedED := nested.integer(0, 65535, "ed")
				if nested.finish() != nil || (ed != 0 && ed != nestedED) {
					return errShareParams
				}
				ed, path = nestedED, path[:idx]
			}
		}
		earlyHeader := q.take("eh", "early-data-header-name")
		if earlyHeader != "" && ed == 0 {
			return errShareParams
		}
		if ed > 0 && earlyHeader == "" {
			earlyHeader = "Sec-WebSocket-Protocol"
		}
		if !validShareString(earlyHeader) {
			return errShareParams
		}
		p.WSOpts = &wsOpts{Path: path, MaxEarlyData: ed, EarlyDataHeaderName: earlyHeader}
		if host != "" {
			p.WSOpts.Headers = map[string]string{"Host": host}
		}
	case "grpc":
		service := q.take("serviceName", "service-name")
		mode := q.take("mode")
		if !validShareString(service) || (mode != "" && mode != "gun") {
			return errShareParams
		}
		p.GrpcOpts = &grpcOpts{GrpcServiceName: service}
	default:
		return errShareParams
	}
	return nil
}
func parseVlessLink(link string) (shareProxy, error) {
	p, u, q, err := parseShareURI(link, 0)
	if err != nil {
		return p, err
	}
	p.Type, p.UUID = "vless", u.User.Username()
	if _, exists := u.User.Password(); exists || !validShareUUID(p.UUID) {
		return p, errShareURL
	}
	encryption := q.take("encryption")
	if encryption != "" && encryption != "none" {
		return p, errShareParams
	}
	security := q.take("security")
	if security == "" {
		security = "none"
	}
	if err := applyShareTLS(&p, q, security); err != nil {
		return p, err
	}
	if err := applyShareTransport(&p, q); err != nil {
		return p, err
	}
	flow := q.take("flow")
	if flow != "" && flow != "none" {
		if flow != "xtls-rprx-vision" || !p.TLS || p.Network != "tcp" {
			return p, errShareParams
		}
		p.Flow = flow
	}
	return p, q.finish()
}
func parseTrojanLink(link string) (shareProxy, error) {
	p, u, q, err := parseShareURI(link, 443)
	if err != nil {
		return p, err
	}
	p.Type, p.Password = "trojan", u.User.Username()
	if _, exists := u.User.Password(); exists || p.Password == "" || !validShareString(p.Password) {
		return p, errShareURL
	}
	security := q.take("security")
	if security == "" {
		security = "tls"
	}
	if security != "tls" {
		return p, errShareParams
	}
	if err := applyShareTLS(&p, q, security); err != nil {
		return p, err
	}
	if err := applyShareTransport(&p, q); err != nil {
		return p, err
	}
	p.SNI, p.Servername = p.Servername, ""
	return p, q.finish()
}

func parseSSLink(link string) (shareProxy, error) {
	body := link[strings.Index(link, "://")+3:]
	// Legacy SS encodes the entire method:password@host:port authority.
	authority := strings.SplitN(strings.SplitN(body, "#", 2)[0], "?", 2)[0]
	if !strings.Contains(authority, "@") {
		decoded, err := shareBase64(strings.TrimSuffix(authority, "/"))
		if err != nil {
			return shareProxy{}, errShareURL
		}
		raw := string(decoded)
		idx := strings.LastIndex(raw, "@")
		colon := strings.Index(raw, ":")
		if idx < 0 || colon < 0 || colon > idx {
			return shareProxy{}, errShareURL
		}
		credentials := url.UserPassword(raw[:colon], raw[colon+1:idx]).String()
		link = "ss://" + credentials + "@" + raw[idx+1:] + body[len(authority):]
	}
	// Standard Base64 userinfo can contain /, which net/url otherwise treats
	// as a path delimiter. Normalize credentials before parsing the URL.
	if at := strings.LastIndex(authority, "@"); at >= 0 && !strings.Contains(authority[:at], ":") {
		decoded, err := shareBase64(authority[:at])
		if err != nil {
			return shareProxy{}, errShareURL
		}
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return shareProxy{}, errShareURL
		}
		link = "ss://" + url.UserPassword(parts[0], parts[1]).String() + authority[at:] + body[len(authority):]
	}
	p, u, q, err := parseShareURI(link, 0)
	if err != nil {
		return p, err
	}
	p.Type = "ss"
	p.Cipher, p.Password = u.User.Username(), ""
	if password, exists := u.User.Password(); exists {
		p.Password = password
	} else {
		decoded, err := shareBase64(u.User.Username())
		if err != nil {
			return p, errShareURL
		}
		credentials := strings.SplitN(string(decoded), ":", 2)
		if len(credentials) != 2 {
			return p, errShareURL
		}
		p.Cipher, p.Password = credentials[0], credentials[1]
	}
	if p.Password == "" || !validShareString(p.Password) {
		return p, errShareURL
	}
	if !shareOneOf(p.Cipher, "aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "xchacha20-ietf-poly1305", "aes-128-cfb", "aes-192-cfb", "aes-256-cfb", "aes-128-ctr", "aes-192-ctr", "aes-256-ctr", "rc4-md5", "chacha20-ietf", "xchacha20", "none", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305") {
		return p, errShareParams
	}
	if strings.HasPrefix(p.Cipher, "2022-") {
		length := 32
		if p.Cipher == "2022-blake3-aes-128-gcm" {
			length = 16
		}
		for _, part := range strings.Split(p.Password, ":") {
			key, err := base64.StdEncoding.Strict().DecodeString(part)
			if err != nil || len(key) != length {
				return p, errShareParams
			}
		}
	}
	if plugin := q.take("plugin"); plugin != "" {
		if err := applySSPlugin(&p, plugin); err != nil {
			return p, err
		}
	}
	return p, q.finish()
}
func applySSPlugin(p *shareProxy, text string) error {
	parts := strings.Split(text, ";")
	q := &shareParams{values: url.Values{}, used: map[string]bool{}}
	for _, option := range parts[1:] {
		if option == "" {
			continue
		}
		item := strings.SplitN(option, "=", 2)
		value := "true"
		if len(item) == 2 {
			value = item[1]
		}
		q.values[item[0]] = append(q.values[item[0]], value)
	}
	p.PluginOpts = map[string]any{}
	switch parts[0] {
	case "obfs-local", "simple-obfs":
		mode := q.take("obfs")
		if mode != "http" && mode != "tls" {
			return errShareParams
		}
		p.Plugin, p.PluginOpts["mode"] = "obfs", mode
		if host := q.take("obfs-host"); host != "" {
			p.PluginOpts["host"] = host
		}

	case "v2ray-plugin":
		mode := q.take("mode")
		if mode != "" && mode != "websocket" {
			return errShareParams
		}
		p.Plugin, p.PluginOpts["mode"] = "v2ray-plugin", "websocket"
		p.PluginOpts["tls"] = q.boolean("tls")
		p.PluginOpts["skip-cert-verify"] = q.boolean("skip-cert-verify")
		if p.PluginOpts["skip-cert-verify"] == true && p.PluginOpts["tls"] != true {
			return errShareParams
		}
		if host := q.take("host"); host != "" {
			p.PluginOpts["host"] = host
		}
		if path := q.take("path"); path != "" {
			p.PluginOpts["path"] = path
		}
		if q.boolean("mux") {
			return errShareParams
		}
	default:
		return errShareParams
	}
	for _, value := range p.PluginOpts {
		if text, ok := value.(string); ok && !validShareString(text) {
			return errShareParams
		}
	}
	return q.finish()
}

func parseVMessLink(link string) (shareProxy, error) {
	body := link[strings.Index(link, "://")+3:]
	if strings.Contains(strings.SplitN(body, "#", 2)[0], "@") {
		return parseVMessURI(link)
	}
	// v2rayN v2 JSON, including number-or-string ports/alter IDs and the four
	// common Base64 alphabets/padding variants. No opaque extensions are ignored.
	fragment := ""
	if idx := strings.IndexByte(body, '#'); idx >= 0 {
		var err error
		fragment, err = url.PathUnescape(body[idx+1:])
		if err != nil {
			return shareProxy{}, errShareURL
		}
		body = body[:idx]
	}
	var data []byte
	if raw, err := url.PathUnescape(body); err == nil && strings.HasPrefix(strings.TrimSpace(raw), "{") {
		data = []byte(raw)
	} else {
		var err error
		data, err = shareBase64(body)
		if err != nil {
			return shareProxy{}, errShareURL
		}
	}
	values, err := shareJSONObject(data)
	if err != nil {
		return shareProxy{}, err
	}
	q := &shareParams{values: values, used: map[string]bool{}}
	p := shareProxy{Type: "vmess"}
	version := q.take("v")
	if version != "" && version != "2" {
		return p, errShareParams
	}
	p.Name = q.take("ps", "remarks")
	if fragment != "" {
		if p.Name != "" && p.Name != fragment {
			return p, errShareParams
		}
		p.Name = fragment
	}
	if !validShareString(p.Name) {
		return p, errShareParams
	}
	if err := setShareEndpoint(&p, strings.Trim(q.take("add"), "[]"), q.take("port"), 0); err != nil {
		return p, err
	}
	if p.Name == "" {
		p.Name = net.JoinHostPort(p.Server, strconv.Itoa(p.Port))
	}
	p.UUID = q.take("id")
	if !validShareUUID(p.UUID) {
		return p, errShareURL
	}
	if _, exists := q.values["udp"]; exists {
		p.UDP = q.boolean("udp")
	}
	alterID := q.integer(0, 65535, "aid")
	p.AlterID = &alterID
	p.Cipher = q.take("scy")
	if p.Cipher == "" {
		p.Cipher = "auto"
	}
	if !shareOneOf(p.Cipher, "auto", "none", "zero", "aes-128-gcm", "chacha20-poly1305") {
		return p, errShareParams
	}
	security := q.take("tls")
	if security == "" {
		security = "none"
	}
	if security != "none" && security != "tls" {
		return p, errShareParams
	}
	if err := applyShareTLS(&p, q, security); err != nil {
		return p, err
	}
	network := q.take("net")
	if network == "" {
		network = "tcp"
	}
	header := q.take("type")
	if header != "" && header != "none" {
		return p, errShareParams
	}
	path, host := q.take("path"), q.take("host")
	if network == "grpc" {
		if host != "" {
			return p, errShareParams
		}
		q.values["serviceName"] = []string{path}
	} else if network == "ws" {
		q.values["path"], q.values["host"] = []string{path}, []string{host}
		delete(q.used, "path")
		delete(q.used, "host")
	} else if path != "" || host != "" {
		return p, errShareParams
	}
	// Remove VMess JSON's header type, which is not its transport name.
	delete(q.values, "type")
	q.values["network"] = []string{network}
	if err := applyShareTransport(&p, q); err != nil {
		return p, err
	}
	return p, q.finish()
}
func shareJSONObject(data []byte) (url.Values, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errShareURL
	}
	values := url.Values{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errShareURL
		}
		if _, exists := values[key]; exists {
			return nil, errShareParams
		}
		var raw any
		if decoder.Decode(&raw) != nil {
			return nil, errShareURL
		}
		switch value := raw.(type) {
		case string:
			values[key] = []string{value}
		case json.Number:
			values[key] = []string{value.String()}
		case bool:
			values[key] = []string{strconv.FormatBool(value)}
		default:
			return nil, errShareParams
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errShareURL
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errShareURL
	}
	return values, nil
}
func parseVMessURI(link string) (shareProxy, error) {
	p, u, q, err := parseShareURI(link, 0)
	if err != nil {
		return p, err
	}
	p.Type, p.UUID = "vmess", u.User.Username()
	if _, exists := u.User.Password(); exists || !validShareUUID(p.UUID) {
		return p, errShareURL
	}
	p.Cipher = q.take("encryption", "cipher")
	if p.Cipher == "" {
		p.Cipher = "auto"
	}
	if !shareOneOf(p.Cipher, "auto", "none", "zero", "aes-128-gcm", "chacha20-poly1305") {
		return p, errShareParams
	}
	alterID := q.integer(0, 65535, "alterId", "aid")
	p.AlterID = &alterID
	security := q.take("security")
	if security == "" {
		security = "none"
	}
	if security != "none" && security != "tls" {
		return p, errShareParams
	}
	if err := applyShareTLS(&p, q, security); err != nil {
		return p, err
	}
	if err := applyShareTransport(&p, q); err != nil {
		return p, err
	}
	return p, q.finish()
}

func parseHysteria2Link(link string) (shareProxy, error) {
	p, u, q, err := parseShareURI(link, 443)
	if err != nil {
		return p, err
	}
	p.Type, p.Password = "hysteria2", u.User.Username()
	if password, exists := u.User.Password(); exists {
		p.Password += ":" + password
	}
	if p.Password == "" || !validShareString(p.Password) {
		return p, errShareURL
	}
	security := q.take("security")
	if security != "" && security != "tls" {
		return p, errShareParams
	}
	if err := applyShareTLS(&p, q, "tls"); err != nil {
		return p, err
	}
	// QUIC does not use uTLS fingerprints; never pretend the fp took effect.
	if p.ClientFingerprint != "" {
		return p, errShareParams
	}
	p.SNI, p.Servername, p.TLS = p.Servername, "", false
	p.Obfs, p.ObfsPassword = q.take("obfs"), q.take("obfs-password", "obfsPassword")
	if p.Obfs != "" && p.Obfs != "salamander" {
		return p, errShareParams
	}
	if (p.Obfs == "") != (p.ObfsPassword == "") || !validShareString(p.ObfsPassword) {
		return p, errShareParams
	}
	p.Up, p.Down = q.take("up", "upmbps"), q.take("down", "downmbps")
	for _, value := range []string{p.Up, p.Down} {
		if value != "" {
			if number, err := strconv.Atoi(value); err != nil || number <= 0 || number > 1000000 {
				return p, errShareParams
			}
		}
	}
	p.Ports = q.take("mport", "ports")
	if p.Ports != "" {
		for _, item := range strings.Split(p.Ports, ",") {
			ends := strings.Split(item, "-")
			if len(ends) > 2 {
				return p, errShareParams
			}
			low := 0
			for _, end := range ends {
				number, err := strconv.Atoi(end)
				if err != nil || number < 1 || number > 65535 || (low != 0 && number < low) {
					return p, errShareParams
				}
				low = number
			}
		}
	}
	p.HopInterval = q.integer(1, 86400, "hop-interval", "hopInterval")
	if p.HopInterval != 0 && p.Ports == "" {
		return p, errShareParams
	}
	return p, q.finish()
}
func parseTUICLink(link string) (shareProxy, error) {
	p, u, q, err := parseShareURI(link, 443)
	if err != nil {
		return p, err
	}
	p.Type, p.UUID = "tuic", u.User.Username()
	p.Password, _ = u.User.Password()
	if !validShareUUID(p.UUID) || p.Password == "" || !validShareString(p.Password) {
		return p, errShareURL
	}
	security := q.take("security")
	if security != "" && security != "tls" {
		return p, errShareParams
	}
	if err := applyShareTLS(&p, q, "tls"); err != nil {
		return p, err
	}
	if p.ClientFingerprint != "" {
		return p, errShareParams
	}
	p.SNI, p.Servername, p.TLS = p.Servername, "", false
	p.CongestionController = q.take("congestion_control", "congestion-control", "congestion-controller")
	if p.CongestionController != "" && !shareOneOf(p.CongestionController, "bbr", "cubic", "new_reno") {
		return p, errShareParams
	}
	p.UDPRelayMode = q.take("udp_relay_mode", "udp-relay-mode")
	if p.UDPRelayMode != "" && !shareOneOf(p.UDPRelayMode, "native", "quic") {
		return p, errShareParams
	}
	p.DisableSNI, p.ReduceRTT = q.boolean("disable_sni", "disable-sni"), q.boolean("reduce_rtt", "reduce-rtt")
	if p.DisableSNI {
		if q.values.Get("sni") != "" || q.values.Get("peer") != "" || q.values.Get("servername") != "" {
			return p, errShareParams
		}
		p.SNI = ""
	}
	return p, q.finish()
}
