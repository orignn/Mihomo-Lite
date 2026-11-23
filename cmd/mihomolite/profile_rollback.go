package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func regularFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// rollbackProfile is called while the profile operation slot is owned. Validate
// the previous content before changing any saved file; the transaction preserves
// the current YAML as the new previous version and leaves subscription identity.
func (a *Application) rollbackProfile(id string) error {
	store, err := a.loadProfileStore()
	if err != nil {
		return errors.New("无法读取配置列表，未恢复上一版")
	}
	index := profileIndex(store, id)
	if index < 0 {
		return errors.New("配置不存在")
	}
	relative := filepath.ToSlash(filepath.Join("profiles", id+".yaml"))
	if ensureBackupTarget(a.dataDir, relative) != nil || ensureBackupTarget(a.dataDir, relative+".bak") != nil {
		return errors.New("配置或上一版文件路径无效，未执行恢复")
	}
	content, err := os.ReadFile(a.profileContentPath(id))
	if err != nil || len(content) == 0 || len(content) > maxBackupYAMLBytes {
		return errors.New("当前配置无法读取，未执行恢复")
	}
	previous, err := os.ReadFile(a.profileContentPath(id) + ".bak")
	if err != nil {
		return errors.New("没有可恢复的上一版配置")
	}
	rules, err := a.loadCustomRules()
	overrides, overrideErr := a.loadOverrides()
	if err != nil || overrideErr != nil || a.validateBackupYAMLWithOverrides(previous, id, rules, &overrides) != nil {
		return errors.New("上一版配置未通过校验，当前配置保持不变；请检查自定义规则兼容性或所需外部资源")
	}
	profile := &store.Profiles[index]
	profile.Size = int64(len(previous))
	profile.UpdatedAt = time.Now().Format(time.RFC3339)
	for i := range store.Profiles {
		p := &store.Profiles[i]
		p.Active, p.Pending, p.HasUpdateDiff, p.HasBackup = p.ID == store.ActiveID, false, false, false
	}
	encoded, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return errors.New("无法准备恢复配置列表")
	}
	changes := []backupFileChange{
		{Path: relative, Content: previous},
		{Path: relative + ".bak", Content: content},
		{Path: filepath.ToSlash(filepath.Join("profiles", id+".subscription-diff.json")), Remove: true},
	}
	if store.ActiveID == id {
		a.mu.Lock()
		running := a.core != nil
		a.mu.Unlock()
		changes = append(changes, backupFileChange{Path: "source.yaml", Content: previous})
		if running {
			changes = append(changes, backupFileChange{Path: "pending-profile", Content: []byte(id)})
		} else {
			changes = append(changes, backupFileChange{Path: "pending-profile", Remove: true})
		}
		settings, err := a.settingsWithProfileName(profile.Name)
		if err != nil {
			return errors.New("无法准备恢复配置设置")
		}
		changes = append(changes, backupFileChange{Path: "@launcher.json", Content: settings})
	}
	changes = append(changes, backupFileChange{Path: "profiles.json", Content: append(encoded, '\n')})
	if err := a.commitBackupFiles(changes); err != nil {
		return errors.New("恢复上一版未完成；已尝试恢复原数据，未完成的事务会保留快照供下次启动恢复")
	}
	if store.ActiveID == id {
		a.mu.Lock()
		if a.config != nil {
			a.config.ConfigName = profile.Name
		}
		a.mu.Unlock()
	}
	a.profileRevision.Add(1)
	return nil
}
