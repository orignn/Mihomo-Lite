package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type subscriptionUpdate struct {
	At      time.Time `json:"at"`
	Status  string    `json:"status"`
	Message string    `json:"message"`
	Auto    bool      `json:"auto"`
}

type subscriptionDiff struct {
	At     time.Time `json:"at"`
	Before string    `json:"before"`
	After  string    `json:"after"`
}

func (a *Application) subscriptionDiffPath(id string) string {
	return filepath.Join(a.profileDir(), id+".subscription-diff.json")
}

func (a *Application) handleSubscriptionDiff(w http.ResponseWriter, r *http.Request) {
	store, err := a.loadProfileStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "无法读取配置列表"})
		return
	}
	id := r.PathValue("id")
	index := profileIndex(store, id)
	if index < 0 || store.Profiles[index].Source != "subscription" {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "订阅不存在"})
		return
	}
	content, err := os.ReadFile(a.subscriptionDiffPath(id))
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "暂无订阅更新差异"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "无法读取订阅更新差异"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(content)
}

func (a *Application) updateSubscription(ctx context.Context, id, mode string) error {
	return a.refreshSubscription(ctx, id, mode, false)
}

func (a *Application) updateSubscriptionURL(ctx context.Context, id, rawURL, mode string) error {
	return a.updateSubscriptionURLWithPolicy(ctx, id, rawURL, mode, nil)
}

func (a *Application) updateSubscriptionURLWithPolicy(ctx context.Context, id, rawURL, mode string, policy *subscriptionUpdatePolicy) error {
	address, err := normalizeSubscriptionURL(rawURL)
	if err != nil {
		return err
	}
	return a.refreshSubscriptionAtWithPolicy(ctx, id, mode, false, address, policy)
}

// The caller owns the application operation slot, including post-commit reconnect.
func (a *Application) refreshSubscription(ctx context.Context, id, mode string, automatic bool) error {
	return a.refreshSubscriptionAt(ctx, id, mode, automatic, "")
}

// The indirection is only replaced by isolated tests; production uses the existing reconnect path.
var applySavedSubscription = func(a *Application, id string) error { return a.reconnectAfterProfileChange(id) }

func (a *Application) applyCommittedSubscription(id string) error {
	a.mu.Lock()
	a.subscriptionApplying = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.subscriptionApplying = false; a.mu.Unlock() }()
	return applySavedSubscription(a, id)
}

type subscriptionCandidate struct {
	before      []byte
	content     []byte
	traffic     *subscriptionTraffic
	cache       *subscriptionDownloadCache
	notModified bool
}

func safeSubscriptionValidationError(err error) error {
	var compatible *customPolicyCompatibilityError
	if errors.As(err, &compatible) {
		return compatible
	}
	return errors.New("订阅内容未通过配置校验，请检查 YAML 结构与所需外部资源；自定义规则会跨配置保留")
}

func (a *Application) prepareSubscriptionCandidate(ctx context.Context, profile Profile, address, mode string) (subscriptionCandidate, error) {
	var candidate subscriptionCandidate
	relative := "profiles/" + profile.ID + ".yaml"
	if ensureBackupTarget(a.dataDir, relative) != nil {
		return candidate, errors.New("当前配置路径无效")
	}
	before, err := os.ReadFile(a.profileContentPath(profile.ID))
	if err != nil || len(before) == 0 || len(before) > 4<<20 {
		return candidate, errors.New("无法读取当前配置")
	}
	download, err := a.downloadSubscriptionWithPolicy(ctx, address, mode, profile.UpdatePolicy, profile.DownloadCache, before)
	if err != nil {
		return candidate, err
	}
	data := before
	if download.NotModified {
		if !trustedSubscriptionCache(profile.DownloadCache, address, effectiveSubscriptionUpdatePolicy(profile.UpdatePolicy).UserAgent, before) {
			return candidate, &subscriptionFetchError{Kind: "cache"}
		}
	} else {
		template := before
		if !hasRichRules(template) {
			template = a.activeProfileTemplate()
		}
		data, err = a.normalizeProfileInput(download.Content, template)
		if err != nil {
			if safe := safeShareLinkConversionError(err); safe != nil {
				return candidate, safe
			}
			return candidate, errors.New("无法解析订阅内容，请使用受支持的完整 Clash/mihomo YAML")
		}
	}
	// A 304 validates the actual saved YAML under today's custom rules and
	// resources, not an empty response body or an unverified historic cache.
	data, err = a.validateProfileContentForID(ctx, profile.ID, data)
	if err != nil {
		if ctx.Err() != nil {
			return candidate, ctx.Err()
		}
		return candidate, safeSubscriptionValidationError(err)
	}
	if err := ctx.Err(); err != nil {
		return candidate, err
	}
	if download.NotModified {
		current, err := os.ReadFile(a.profileContentPath(profile.ID))
		if err != nil || !bytes.Equal(current, before) {
			return candidate, &subscriptionFetchError{Kind: "cache"}
		}
	}
	traffic := download.Traffic
	if download.NotModified && traffic == nil {
		traffic = profile.Traffic
	}
	return subscriptionCandidate{before: before, content: data, traffic: traffic, cache: download.cacheForContent(data), notModified: download.NotModified}, nil
}

func setSubscriptionResult(profile *Profile, at time.Time, status, message string, automatic bool) {
	profile.LastUpdate = &subscriptionUpdate{At: at, Status: status, Message: message, Auto: automatic}
	profile.NextUpdateAt = subscriptionNextTime(at, profile.AutoUpdateHours)
	setSubscriptionRetry(profile, at, status != "failed", automatic)
	if profile.UpdateRetry != nil {
		profile.NextUpdateAt = profile.UpdateRetry.RetryAt.Format(time.RFC3339Nano)
	}
}

func (a *Application) recordSubscriptionFailure(store profileStore, index int, automatic bool, cause error) error {
	setSubscriptionResult(&store.Profiles[index], time.Now(), "failed", cause.Error(), automatic)
	if err := a.saveProfileStore(store); err != nil {
		return errors.New("更新失败，旧配置保持不变；无法保存本次失败记录")
	}
	return fmt.Errorf("更新失败，旧配置保持不变: %w", cause)
}

func (a *Application) refreshSubscriptionAt(ctx context.Context, id, mode string, automatic bool, newAddress string) error {
	return a.refreshSubscriptionAtWithPolicy(ctx, id, mode, automatic, newAddress, nil)
}

func (a *Application) refreshSubscriptionAtWithPolicy(ctx context.Context, id, mode string, automatic bool, newAddress string, policy *subscriptionUpdatePolicy) error {
	policy, err := normalizeSubscriptionUpdatePolicy(policy)
	if err != nil {
		return err
	}
	store, err := a.loadProfileStore()
	if err != nil {
		return errors.New("无法读取配置列表，未更新订阅")
	}
	index := profileIndex(store, id)
	if index < 0 {
		return errors.New("配置不存在")
	}
	original := store.Profiles[index]
	if original.Source != "subscription" {
		return errors.New("本地 YAML 配置没有订阅地址")
	}
	if ensureBackupTarget(a.dataDir, "profiles.json") != nil {
		return errors.New("配置列表路径无效，未更新订阅")
	}
	address := original.URL
	editing := newAddress != "" || policy != nil
	if newAddress != "" {
		address = newAddress
	}
	candidateProfile := original
	if policy != nil {
		candidateProfile.UpdatePolicy = policy
	}
	candidate, err := a.prepareSubscriptionCandidate(ctx, candidateProfile, address, mode)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if editing {
			return fmt.Errorf("修改失败，旧订阅地址、策略与配置保持不变: %w", err)
		}
		return a.recordSubscriptionFailure(store, index, automatic, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	changed := !bytes.Equal(candidate.before, candidate.content)
	updated := time.Now()
	profile := &store.Profiles[index]
	profile.URL, profile.Traffic = address, candidate.traffic
	if !candidate.notModified {
		profile.TrafficScanned = true
	}
	profile.UpdatePolicy, profile.DownloadCache = candidateProfile.UpdatePolicy, candidate.cache
	profile.Size = int64(len(candidate.content))
	if changed || address != original.URL {
		profile.UpdatedAt = updated.Format(time.RFC3339)
	}
	status, message := "success", "订阅已更新"
	if !changed {
		status, message = "unchanged", "订阅内容无变化"
	}
	if candidate.notModified {
		message = "订阅内容无变化（HTTP 304）"
	}
	if editing {
		message = "订阅地址与策略已保存并校验；" + message
	}
	setSubscriptionResult(profile, updated, status, message, automatic)

	a.mu.Lock()
	running := a.core != nil
	a.mu.Unlock()
	pending, pendingErr := os.ReadFile(a.pendingProfilePath())
	needsApply := !candidate.notModified && original.ID == store.ActiveID && (changed || (pendingErr == nil && strings.TrimSpace(string(pending)) == original.ID))
	current, readErr := os.ReadFile(a.profileContentPath(original.ID))
	if readErr != nil || !bytes.Equal(current, candidate.before) {
		return errors.New("更新期间本地配置已改变，未提交订阅内容或元信息，请重试")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !changed {
		// Only metadata changes: one atomic file, no snapshot of all subscriptions.
		if err := a.saveProfileStore(store); err != nil {
			return errors.New("无法保存订阅信息，旧地址与配置保持不变")
		}
	} else {
		relative := "profiles/" + original.ID + ".yaml"
		diff, err := json.Marshal(subscriptionDiff{At: updated, Before: string(candidate.before), After: string(candidate.content)})
		if err != nil {
			return errors.New("无法准备订阅差异，未更改订阅")
		}
		encoded, err := encodeProfileStore(store)
		if err != nil {
			return errors.New("无法准备订阅信息，未更改订阅")
		}
		changes := []backupFileChange{
			{Path: relative + ".bak", Content: candidate.before},
			{Path: relative, Content: candidate.content},
			{Path: "profiles/" + original.ID + ".subscription-diff.json", Content: diff},
		}
		if original.ID == store.ActiveID {
			changes = append(changes, backupFileChange{Path: "source.yaml", Content: candidate.content})
			if running {
				changes = append(changes, backupFileChange{Path: "pending-profile", Content: []byte(original.ID)})
			} else {
				changes = append(changes, backupFileChange{Path: "pending-profile", Remove: true})
			}
		}
		changes = append(changes, backupFileChange{Path: "profiles.json", Content: encoded})
		// Once commit starts it must finish or roll back, even if ctx is cancelled.
		if err := ctx.Err(); err != nil {
			return err
		}
		if editing {
			err = a.commitBackupFiles(changes)
		} else {
			err = a.commitSubscriptionFiles(changes)
		}
		if err != nil {
			cause := errors.New("订阅提交未完成；已尝试回退旧数据，未完成事务将保留恢复快照供下次启动恢复")
			a.mu.Lock()
			blocked := a.backupRecoveryBlocked
			a.mu.Unlock()
			if editing || blocked {
				return cause
			}
			store.Profiles[index] = original
			return a.recordSubscriptionFailure(store, index, automatic, cause)
		}
		a.profileRevision.Add(1)
	}

	// Applying the already committed configuration is distinct from a rejected
	// URL edit. Never claim old data was retained when only reconnect failed.
	if needsApply {
		if ctx.Err() != nil {
			message = "订阅地址及配置已保存，退出中未重连；下次连接时生效"
			setSubscriptionResult(profile, time.Now(), "saved", message, automatic)
			_ = a.saveProfileStore(store)
			return errors.New(message)
		}
		if err := a.applyCommittedSubscription(original.ID); err != nil {
			message = "订阅地址及配置已保存，但应用失败，请重新连接"
			setSubscriptionResult(profile, time.Now(), "saved", message, automatic)
			_ = a.saveProfileStore(store)
			return errors.New(message)
		}
	}
	if a.logger != nil {
		a.logger.Info("订阅更新已保存", "bytes", len(candidate.content), "automatic", automatic)
	}
	return nil
}

func subscriptionNextTime(now time.Time, hours int) string {
	if hours <= 0 {
		return ""
	}
	return now.Add(time.Duration(hours) * time.Hour).Format(time.RFC3339Nano)
}

func (a *Application) setSubscriptionAutoUpdate(id string, hours *int, now time.Time) error {
	if hours == nil || (*hours != 0 && *hours != 6 && *hours != 12 && *hours != 24 && *hours != 168) {
		return errors.New("请选择关闭、每 6 小时、每 12 小时、每天或每周更新")
	}
	store, err := a.loadProfileStore()
	if err != nil {
		return err
	}
	index := profileIndex(store, id)
	if index < 0 || store.Profiles[index].Source != "subscription" {
		return errors.New("订阅不存在")
	}
	profile := &store.Profiles[index]
	profile.AutoUpdateHours = *hours
	profile.UpdateRetry = nil
	profile.NextUpdateAt = subscriptionNextTime(now, *hours)
	return a.saveProfileStore(store)
}

func (a *Application) dueSubscriptions(now time.Time) []string {
	store, err := a.loadProfileStore()
	if err != nil {
		return nil
	}
	var ids []string
	for _, profile := range store.Profiles {
		if profile.Source != "subscription" || profile.AutoUpdateHours <= 0 {
			continue
		}
		next, err := time.Parse(time.RFC3339Nano, profile.NextUpdateAt)
		if err == nil && !next.After(now) {
			ids = append(ids, profile.ID)
		}
	}
	return ids
}

func (a *Application) runDueSubscriptionUpdates(ctx context.Context, now time.Time) {
	for _, id := range a.dueSubscriptions(now) {
		if ctx.Err() != nil || a.begin() != nil {
			return
		}
		var err error
		// An update may have been disabled or run manually before we acquired the slot.
		for _, due := range a.dueSubscriptions(now) {
			if due != id {
				continue
			}
			// Empty mode deliberately resolves this subscription's persisted policy.
			err = a.refreshSubscription(ctx, id, "", true)
			break
		}
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			err = nil
		}
		a.finish(err)
	}
}

func (a *Application) runSubscriptionUpdates(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	a.runDueSubscriptionUpdates(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.runDueSubscriptionUpdates(ctx, now)
		}
	}
}
