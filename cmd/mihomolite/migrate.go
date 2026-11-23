package main

import (
	"os"
	"path/filepath"
	"time"
)

// migrateLegacySource imports an existing v0.2 configuration without changing it.
func (a *Application) migrateLegacySource() error {
	if fileExists(a.profileStorePath()) {
		return nil
	}
	content, err := os.ReadFile(filepath.Join(a.dataDir, "source.yaml"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	id := newProfileID(content)
	if err := atomicWrite(a.profileContentPath(id), content, 0o600); err != nil {
		return err
	}
	name := a.config.ConfigName
	if name == "" {
		name = "本地 YAML 配置"
	}
	now := time.Now().Format(time.RFC3339)
	store := profileStore{ActiveID: id, Profiles: []Profile{{ID: id, Name: name, Source: "local", CreatedAt: now, UpdatedAt: now, Size: int64(len(content))}}}
	return a.saveProfileStore(store)
}
