package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSubscriptionUserAgent = "clash-verge/v2.2.3"
	subscriptionDownloadBudget   = 45 * time.Second
	subscriptionRouteBudget      = 20 * time.Second
	maxSubscriptionFailures      = 64
)

type subscriptionUpdatePolicy struct {
	Mode           string `json:"mode"`
	UserAgent      string `json:"userAgent,omitempty"`
	Conditional    bool   `json:"conditional"`
	FailureBackoff bool   `json:"failureBackoff"`
}

// Only automatic failures advance this durable state. Manual downloads ignore
// RetryAt; any successful download (including a trusted 304) clears the state.
type subscriptionUpdateRetry struct {
	ConsecutiveFailures int       `json:"consecutiveFailures"`
	LastFailureAt       time.Time `json:"lastFailureAt"`
	RetryAt             time.Time `json:"retryAt"`
}

// This is private on-disk metadata, never a profile API/backup field. Hashes
// avoid retaining redirect query credentials in addition to the original URL.
// Validators are usable only for this exact resource and committed YAML digest.
type subscriptionDownloadCache struct {
	URLSHA256       string `json:"urlSHA256"`
	UserAgentSHA256 string `json:"userAgentSHA256"`
	ResourceSHA256  string `json:"resourceSHA256"`
	ContentSHA256   string `json:"contentSHA256"`
	ETag            string `json:"etag,omitempty"`
	LastModified    string `json:"lastModified,omitempty"`
}

func validSubscriptionUpdatePolicy(policy *subscriptionUpdatePolicy) bool {
	if policy == nil {
		return true
	}
	if policy.Mode != "auto" && policy.Mode != "direct" && policy.Mode != "proxy" {
		return false
	}
	if len(policy.UserAgent) > 512 {
		return false
	}
	for _, c := range policy.UserAgent {
		if c < 32 || c > 126 {
			return false
		}
	}
	return policy.UserAgent == strings.TrimSpace(policy.UserAgent)
}

func normalizeSubscriptionUpdatePolicy(policy *subscriptionUpdatePolicy) (*subscriptionUpdatePolicy, error) {
	if policy == nil {
		return nil, nil
	}
	copy := *policy
	copy.UserAgent = strings.Trim(copy.UserAgent, " ")
	if !validSubscriptionUpdatePolicy(&copy) {
		return nil, errors.New("订阅更新策略无效；请选择自动、直连或当前代理，UA 须为不超过 512 字节的可打印文本")
	}
	return &copy, nil
}

func effectiveSubscriptionUpdatePolicy(policy *subscriptionUpdatePolicy) subscriptionUpdatePolicy {
	out := subscriptionUpdatePolicy{Mode: "auto", Conditional: true, FailureBackoff: true}
	if policy != nil && validSubscriptionUpdatePolicy(policy) {
		out = *policy
	}
	if out.UserAgent == "" {
		out.UserAgent = defaultSubscriptionUserAgent
	}
	return out
}

func validSubscriptionUpdateRetry(retry *subscriptionUpdateRetry) bool {
	if retry == nil {
		return true
	}
	if retry.ConsecutiveFailures < 1 || retry.ConsecutiveFailures > maxSubscriptionFailures || retry.LastFailureAt.IsZero() || retry.LastFailureAt.Year() < 1970 || retry.LastFailureAt.Year() > 9999 || retry.RetryAt.Year() < 1970 || retry.RetryAt.Year() > 9999 {
		return false
	}
	delay := retry.RetryAt.Sub(retry.LastFailureAt)
	return delay >= time.Minute && delay <= time.Hour
}

func validSubscriptionETag(value string) bool {
	if len(value) > 1024 {
		return false
	}
	if strings.HasPrefix(value, "W/") {
		value = value[2:]
	}
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, c := range value[1 : len(value)-1] {
		if c < 33 || c > 126 || c == '"' {
			return false
		}
	}
	return true
}

func validSubscriptionLastModified(value string) bool {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	at, err := http.ParseTime(value)
	return err == nil && at.Year() >= 1970 && at.Year() <= 9999
}

func validSubscriptionDownloadCache(cache *subscriptionDownloadCache) bool {
	if cache == nil {
		return true
	}
	for _, digest := range []string{cache.URLSHA256, cache.UserAgentSHA256, cache.ResourceSHA256, cache.ContentSHA256} {
		if !validProfileID(digest) || digest != strings.ToLower(digest) {
			return false
		}
	}
	return (cache.ETag != "" || cache.LastModified != "") && (cache.ETag == "" || validSubscriptionETag(cache.ETag)) && (cache.LastModified == "" || validSubscriptionLastModified(cache.LastModified))
}

func decodeSubscriptionMetadata(raw json.RawMessage, target any, maxBytes int) bool {
	if len(raw) == 0 || len(raw) > maxBytes || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(new(any)) == io.EOF
}

// Optional policy/cache/retry corruption must not reject usable saved YAML or
// expose a malformed private validator through a JSON/time parsing error.
func (profile *Profile) UnmarshalJSON(raw []byte) error {
	type plainProfile Profile
	var plain plainProfile
	fields := struct {
		*plainProfile
		UpdatePolicy  json.RawMessage `json:"updatePolicy"`
		UpdateRetry   json.RawMessage `json:"updateRetry"`
		DownloadCache json.RawMessage `json:"downloadCache"`
	}{plainProfile: &plain}
	if json.Unmarshal(raw, &fields) != nil {
		return errors.New("配置元数据格式无效")
	}
	*profile = Profile(plain)
	var policy subscriptionUpdatePolicy
	if decodeSubscriptionMetadata(fields.UpdatePolicy, &policy, 2048) && validSubscriptionUpdatePolicy(&policy) {
		profile.UpdatePolicy = &policy
	}
	var retry subscriptionUpdateRetry
	if decodeSubscriptionMetadata(fields.UpdateRetry, &retry, 2048) && validSubscriptionUpdateRetry(&retry) {
		profile.UpdateRetry = &retry
	}
	var cache subscriptionDownloadCache
	if decodeSubscriptionMetadata(fields.DownloadCache, &cache, 8192) && validSubscriptionDownloadCache(&cache) {
		profile.DownloadCache = &cache
	}
	return nil
}

func validSubscriptionAutoHours(hours int) bool {
	return hours == 0 || hours == 6 || hours == 12 || hours == 24 || hours == 168
}

func validSubscriptionProfileMetadata(profile Profile) bool {
	if !validSubscriptionUpdatePolicy(profile.UpdatePolicy) || !validSubscriptionUpdateRetry(profile.UpdateRetry) || !validSubscriptionDownloadCache(profile.DownloadCache) {
		return false
	}
	if profile.Source != "subscription" {
		return profile.UpdatePolicy == nil && profile.UpdateRetry == nil && profile.DownloadCache == nil
	}
	if retry := profile.UpdateRetry; retry != nil {
		return validSubscriptionAutoHours(profile.AutoUpdateHours) && profile.AutoUpdateHours > 0 && effectiveSubscriptionUpdatePolicy(profile.UpdatePolicy).FailureBackoff && profile.NextUpdateAt == retry.RetryAt.Format(time.RFC3339Nano) && retry.RetryAt.Sub(retry.LastFailureAt) <= time.Duration(profile.AutoUpdateHours)*time.Hour
	}
	return true
}

func sanitizeSubscriptionMetadata(profile *Profile, now time.Time) {
	if profile.Source != "subscription" {
		profile.TrafficScanned = false
		profile.UpdatePolicy, profile.UpdateRetry, profile.DownloadCache = nil, nil, nil
		profile.AutoUpdateHours, profile.NextUpdateAt = 0, ""
		return
	}
	if !validSubscriptionUpdatePolicy(profile.UpdatePolicy) {
		profile.UpdatePolicy = nil
	}
	if !validSubscriptionDownloadCache(profile.DownloadCache) {
		profile.DownloadCache = nil
	}
	if !validSubscriptionAutoHours(profile.AutoUpdateHours) {
		profile.AutoUpdateHours = 0
	}
	if profile.AutoUpdateHours > 0 {
		next, err := time.Parse(time.RFC3339Nano, profile.NextUpdateAt)
		if err != nil || next.Year() < 1970 || next.Year() > 9999 {
			profile.AutoUpdateHours = 0
		}
	}
	if profile.AutoUpdateHours == 0 {
		profile.NextUpdateAt, profile.UpdateRetry = "", nil
		return
	}
	if !effectiveSubscriptionUpdatePolicy(profile.UpdatePolicy).FailureBackoff || !validSubscriptionUpdateRetry(profile.UpdateRetry) || (profile.UpdateRetry != nil && (profile.UpdateRetry.LastFailureAt.After(now.Add(5*time.Minute)) || profile.UpdateRetry.RetryAt.Sub(profile.UpdateRetry.LastFailureAt) > time.Duration(profile.AutoUpdateHours)*time.Hour)) {
		profile.UpdateRetry = nil
	} else if profile.UpdateRetry != nil {
		// The one existing scheduler and the public/backup due time agree.
		profile.NextUpdateAt = profile.UpdateRetry.RetryAt.Format(time.RFC3339Nano)
	}
}

func subscriptionFailureDelay(failures, hours int) time.Duration {
	delay := 5 * time.Minute
	switch {
	case failures >= 4:
		delay = time.Hour
	case failures == 3:
		delay = 30 * time.Minute
	case failures == 2:
		delay = 15 * time.Minute
	}
	if normal := time.Duration(hours) * time.Hour; normal > 0 && delay > normal {
		delay = normal
	}
	return delay
}

func setSubscriptionRetry(profile *Profile, at time.Time, success, automatic bool) {
	if success || profile.AutoUpdateHours <= 0 || !effectiveSubscriptionUpdatePolicy(profile.UpdatePolicy).FailureBackoff {
		profile.UpdateRetry = nil
		return
	}
	if !automatic {
		return
	}
	failures := 1
	if validSubscriptionUpdateRetry(profile.UpdateRetry) && profile.UpdateRetry != nil {
		failures = profile.UpdateRetry.ConsecutiveFailures + 1
	}
	if failures > maxSubscriptionFailures {
		failures = maxSubscriptionFailures
	}
	profile.UpdateRetry = &subscriptionUpdateRetry{ConsecutiveFailures: failures, LastFailureAt: at.UTC(), RetryAt: at.UTC().Add(subscriptionFailureDelay(failures, profile.AutoUpdateHours))}
}

// Policy-only edits reset retry/cache and return to the normal cadence. They
// never download, change successful content timestamps, YAML, pending or source.
func (a *Application) setSubscriptionUpdatePolicy(id string, policy *subscriptionUpdatePolicy) error {
	if policy == nil {
		return errors.New("请提交订阅更新策略")
	}
	policy, err := normalizeSubscriptionUpdatePolicy(policy)
	if err != nil {
		return err
	}
	store, err := a.loadProfileStore()
	if err != nil {
		return errors.New("无法读取配置列表，未保存订阅策略")
	}
	index := profileIndex(store, id)
	if index < 0 || store.Profiles[index].Source != "subscription" {
		return errors.New("订阅不存在")
	}
	if ensureBackupTarget(a.dataDir, "profiles.json") != nil {
		return errors.New("配置列表路径无效，未保存订阅策略")
	}
	profile := &store.Profiles[index]
	profile.UpdatePolicy, profile.UpdateRetry, profile.DownloadCache = policy, nil, nil
	profile.NextUpdateAt = subscriptionNextTime(time.Now(), profile.AutoUpdateHours)
	if err := a.saveProfileStore(store); err != nil {
		return errors.New("无法保存订阅策略，原策略保持不变")
	}
	return nil
}

// Error fields contain only a fixed category and an HTTP status, never an URL,
// redirect Location, validator, response header, or underlying transport error.
type subscriptionFetchError struct {
	Kind       string
	StatusCode int
}

func (err *subscriptionFetchError) Error() string {
	switch err.Kind {
	case "http":
		return fmt.Sprintf("订阅服务器返回 HTTP %d", err.StatusCode)
	case "timeout":
		return "下载订阅超时"
	case "proxy-unavailable":
		return "使用当前代理下载前，请先连接"
	case "redirect":
		return "订阅重定向不安全、协议不受支持或次数过多"
	case "tls":
		return "订阅服务器 TLS 安全校验失败"
	case "content":
		return "订阅响应为空、不完整或超过 4 MiB"
	case "cache":
		return "订阅返回无法安全确认的 HTTP 304，未更改配置"
	case "request":
		return "订阅请求无效"
	default:
		return "无法连接订阅服务器，请检查网络或下载方式"
	}
}

func recoverableSubscriptionFailure(err error) bool {
	var fetch *subscriptionFetchError
	if !errors.As(err, &fetch) {
		return false
	}
	return fetch.Kind == "network" || fetch.Kind == "timeout" || (fetch.Kind == "http" && (fetch.StatusCode == 500 || fetch.StatusCode == 502 || fetch.StatusCode == 503 || fetch.StatusCode == 504))
}

func safeSubscriptionFetchError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var fetch *subscriptionFetchError
	if errors.As(err, &fetch) {
		return fetch
	}
	var invalid x509.CertificateInvalidError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var record tls.RecordHeaderError
	if errors.As(err, &invalid) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &record) {
		return &subscriptionFetchError{Kind: "tls"}
	}
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return &subscriptionFetchError{Kind: "timeout"}
	}
	return &subscriptionFetchError{Kind: "network"}
}

type subscriptionPolicyDownload struct {
	subscriptionDownload
	NotModified bool
	Cache       *subscriptionDownloadCache
}

func (download subscriptionPolicyDownload) cacheForContent(content []byte) *subscriptionDownloadCache {
	if download.Cache == nil {
		return nil
	}
	cache := *download.Cache
	cache.ContentSHA256 = sha256Hex(content)
	if !validSubscriptionDownloadCache(&cache) {
		return nil
	}
	return &cache
}

func subscriptionResourceHash(address string) string {
	clean, err := normalizeSubscriptionURL(address)
	if err != nil {
		return ""
	}
	return sha256Hex([]byte(clean))
}

func trustedSubscriptionCache(cache *subscriptionDownloadCache, address, userAgent string, content []byte) bool {
	return cache != nil && validSubscriptionDownloadCache(cache) && len(content) > 0 && len(content) <= 4<<20 && cache.URLSHA256 == subscriptionResourceHash(address) && cache.UserAgentSHA256 == sha256Hex([]byte(userAgent)) && cache.ContentSHA256 == sha256Hex(content)
}

func subscriptionResponseAllowsCache(header http.Header) bool {
	for _, value := range header.Values("Cache-Control") {
		for _, directive := range strings.Split(value, ",") {
			name, _, _ := strings.Cut(directive, "=")
			if strings.EqualFold(strings.TrimSpace(name), "no-store") {
				return false
			}
		}
	}
	for _, value := range header.Values("Vary") {
		for _, field := range strings.Split(value, ",") {
			if strings.TrimSpace(field) == "*" {
				return false
			}
		}
	}
	return true
}

func subscriptionResponseValidator(header http.Header, name string) string {
	values := header.Values(name)
	if len(values) != 1 {
		return ""
	}
	value := strings.TrimSpace(values[0])
	if name == "ETag" {
		if validSubscriptionETag(value) {
			return value
		}
		return ""
	}
	if validSubscriptionLastModified(value) {
		at, _ := http.ParseTime(value)
		return at.UTC().Format(http.TimeFormat)
	}
	return ""
}

func setSubscriptionConditionalHeaders(req *http.Request, cache *subscriptionDownloadCache) {
	req.Header.Del("If-None-Match")
	req.Header.Del("If-Modified-Since")
	if cache == nil || subscriptionResourceHash(req.URL.String()) != cache.ResourceSHA256 {
		return
	}
	if cache.ETag != "" {
		req.Header.Set("If-None-Match", cache.ETag)
	}
	if cache.LastModified != "" {
		req.Header.Set("If-Modified-Since", cache.LastModified)
	}
}

func (a *Application) currentSubscriptionProxy() *url.URL {
	a.mu.Lock()
	session, port := a.core, a.runtime.MixedPort
	pid, running := a.corePID, false
	if !a.shuttingDown && session != nil && session.ready && !session.stopping && session.done != nil && session.cmd != nil && session.cmd.Process != nil && session.pid == pid && pid > 0 && session.cmd.Process.Pid == pid {
		select {
		case <-session.done:
		default:
			running = true
		}
	}
	a.mu.Unlock()
	if !running || port <= 0 || port > 65535 || !processExists(pid) {
		return nil
	}
	return &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}
}

func (a *Application) downloadSubscriptionWithPolicy(parent context.Context, address, mode string, policy *subscriptionUpdatePolicy, cache *subscriptionDownloadCache, savedContent []byte) (subscriptionPolicyDownload, error) {
	if parent.Err() != nil {
		return subscriptionPolicyDownload{}, parent.Err()
	}
	address, err := normalizeSubscriptionURL(address)
	if err != nil {
		return subscriptionPolicyDownload{}, err
	}
	policy, err = normalizeSubscriptionUpdatePolicy(policy)
	if err != nil {
		return subscriptionPolicyDownload{}, err
	}
	effective := effectiveSubscriptionUpdatePolicy(policy)
	if mode == "" {
		mode = effective.Mode
	}
	if mode != "auto" && mode != "direct" && mode != "proxy" {
		return subscriptionPolicyDownload{}, errors.New("请选择自动、直连或当前代理下载")
	}
	ctx, cancel := context.WithTimeout(parent, subscriptionDownloadBudget)
	defer cancel()
	proxy := a.currentSubscriptionProxy()
	if mode == "proxy" && proxy == nil {
		return subscriptionPolicyDownload{}, &subscriptionFetchError{Kind: "proxy-unavailable"}
	}
	routes := []*url.URL{nil}
	if mode == "proxy" {
		routes = []*url.URL{proxy}
	}
	if mode == "auto" && proxy != nil {
		routes = []*url.URL{proxy, nil}
	}
	var conditional *subscriptionDownloadCache
	if effective.Conditional && trustedSubscriptionCache(cache, address, effective.UserAgent, savedContent) {
		copy := *cache
		conditional = &copy
	}
	var result subscriptionPolicyDownload
	for i, route := range routes {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result, err = downloadSubscriptionRoute(ctx, address, effective, route, conditional)
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return subscriptionPolicyDownload{}, ctx.Err()
		}
		if mode != "auto" || i+1 == len(routes) || !recoverableSubscriptionFailure(err) {
			return subscriptionPolicyDownload{}, err
		}
	}
	return subscriptionPolicyDownload{}, err
}

type subscriptionHTTPTransport struct{ transport *http.Transport }

func (transport subscriptionHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := transport.transport.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		locations := resp.Header.Values("Location")
		if len(locations) == 0 {
			return resp, nil
		}
		unsafe := len(locations) != 1 || len(locations[0]) > 8192
		if !unsafe {
			destination, parseErr := req.URL.Parse(locations[0])
			if parseErr != nil {
				unsafe = true
			} else {
				_, validationErr := normalizeSubscriptionURL(destination.String())
				unsafe = validationErr != nil || (req.URL.Scheme == "https" && destination.Scheme != "https")
			}
		}
		if unsafe {
			_ = resp.Body.Close()
			return nil, &subscriptionFetchError{Kind: "redirect"}
		}
	}
	return resp, nil
}

func downloadSubscriptionRoute(total context.Context, address string, policy subscriptionUpdatePolicy, proxy *url.URL, conditional *subscriptionDownloadCache) (subscriptionPolicyDownload, error) {
	ctx, cancel := context.WithTimeout(total, subscriptionRouteBudget)
	defer cancel()
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return subscriptionPolicyDownload{}, &subscriptionFetchError{Kind: "request"}
	}
	transport := base.Clone()
	transport.Proxy = nil // Never inherit the system/environment proxy in direct mode.
	if proxy != nil {
		transport.Proxy = http.ProxyURL(proxy)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: subscriptionHTTPTransport{transport: transport}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Referer")
		if _, err := normalizeSubscriptionURL(req.URL.String()); err != nil || len(via) >= 5 || (len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https") {
			return &subscriptionFetchError{Kind: "redirect"}
		}
		// net/http copies original headers on redirects, even same-host changes.
		// Rebuild them only for the exact previously committed final resource.
		setSubscriptionConditionalHeaders(req, conditional)
		return nil
	}}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return subscriptionPolicyDownload{}, &subscriptionFetchError{Kind: "request"}
		}
		req.Header.Set("User-Agent", policy.UserAgent)
		req.Header.Set("Accept", "text/yaml, application/yaml, application/x-yaml, text/plain, */*")
		setSubscriptionConditionalHeaders(req, conditional)
		resp, err := client.Do(req)
		if err != nil {
			return subscriptionPolicyDownload{}, safeSubscriptionFetchError(total, err)
		}
		resource := subscriptionResourceHash(resp.Request.URL.String())
		if resp.StatusCode == http.StatusNotModified {
			_ = resp.Body.Close()
			requested := conditional != nil && resource == conditional.ResourceSHA256 && ((conditional.ETag != "" && resp.Request.Header.Get("If-None-Match") == conditional.ETag) || (conditional.LastModified != "" && resp.Request.Header.Get("If-Modified-Since") == conditional.LastModified))
			if !requested {
				// A broken server may send 304 to an unconditional request. Retry
				// once without validators, within the same route/overall budget.
				conditional = nil
				if attempt == 0 {
					continue
				}
				return subscriptionPolicyDownload{}, &subscriptionFetchError{Kind: "cache"}
			}
			cache := *conditional
			if len(resp.Header.Values("ETag")) > 0 {
				cache.ETag = subscriptionResponseValidator(resp.Header, "ETag")
			}
			if len(resp.Header.Values("Last-Modified")) > 0 {
				cache.LastModified = subscriptionResponseValidator(resp.Header, "Last-Modified")
			}
			var next *subscriptionDownloadCache
			if subscriptionResponseAllowsCache(resp.Header) && validSubscriptionDownloadCache(&cache) {
				next = &cache
			}
			return subscriptionPolicyDownload{NotModified: true, Cache: next, subscriptionDownload: subscriptionDownload{Traffic: parseSubscriptionInfo(strings.Join(resp.Header.Values("Subscription-Userinfo"), ";"), nil)}}, nil
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return subscriptionPolicyDownload{}, &subscriptionFetchError{Kind: "http", StatusCode: resp.StatusCode}
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		_ = resp.Body.Close()
		if readErr != nil {
			return subscriptionPolicyDownload{}, safeSubscriptionFetchError(total, readErr)
		}
		if len(data) == 0 || len(data) > 4<<20 {
			return subscriptionPolicyDownload{}, &subscriptionFetchError{Kind: "content"}
		}
		var next *subscriptionDownloadCache
		if policy.Conditional && subscriptionResponseAllowsCache(resp.Header) {
			etag, modified := subscriptionResponseValidator(resp.Header, "ETag"), subscriptionResponseValidator(resp.Header, "Last-Modified")
			if etag != "" || modified != "" {
				next = &subscriptionDownloadCache{URLSHA256: subscriptionResourceHash(address), UserAgentSHA256: sha256Hex([]byte(policy.UserAgent)), ResourceSHA256: resource, ETag: etag, LastModified: modified}
			}
		}
		return subscriptionPolicyDownload{Cache: next, subscriptionDownload: subscriptionDownload{Content: data, Traffic: parseSubscriptionInfo(strings.Join(resp.Header.Values("Subscription-Userinfo"), ";"), data)}}, nil
	}
	return subscriptionPolicyDownload{}, &subscriptionFetchError{Kind: "cache"}
}
