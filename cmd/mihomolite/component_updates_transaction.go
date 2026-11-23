package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The envelope detects corruption; it is not a cryptographic signature. Local
// recovery is constrained to the fixed component subtree and its recorded hashes.
// A local administrator replacing both journal and files is outside this trust.
type componentTransaction struct {
	SchemaVersion    int              `json:"schemaVersion"`
	ID               string           `json:"id"`
	Component        string           `json:"component"`
	State            string           `json:"state"`
	CreatedAt        string           `json:"createdAt"`
	Release          componentRelease `json:"release"`
	InstalledVersion string           `json:"installedVersion,omitempty"`
	Before           []componentFile  `json:"before"`
	After            []componentFile  `json:"after"`
	BeforeExists     bool             `json:"beforeExists"`
	AfterExists      bool             `json:"afterExists"`
	PreviousVersion  string           `json:"previousVersion,omitempty"`
	RollbackOf       string           `json:"rollbackOf,omitempty"`
}
type componentJournal struct {
	SHA256      string          `json:"sha256"`
	Transaction json.RawMessage `json:"transaction"`
}

// Test-only simulated interruption skips normal rollback to exercise startup
// recovery. This sentinel is never reachable from API or runtime configuration.
var errComponentInterrupted = errors.New("simulated component transaction interruption")

func (m *componentUpdateManager) failpoint(point string) error {
	if m.fault != nil {
		return m.fault(point)
	}
	return nil
}
func (m *componentUpdateManager) writeTransaction(tx componentTransaction) error {
	p, err := m.path(componentArchiveRoot + "/" + tx.ID + "/manifest.json")
	if err != nil {
		return err
	}
	b, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	h := sha256.Sum256(b)
	return componentWriteJSON(p, componentJournal{SHA256: hex.EncodeToString(h[:]), Transaction: b})
}
func (m *componentUpdateManager) readTransaction(id string) (componentTransaction, error) {
	var tx componentTransaction
	if !componentIDRE.MatchString(id) {
		return tx, errComponentTrust
	}
	p, err := m.path(componentArchiveRoot + "/" + id + "/manifest.json")
	if err != nil {
		return tx, err
	}
	var journal componentJournal
	if err = componentReadJSONStrict(p, &journal); err != nil {
		return tx, err
	}
	// Hash the struct form used by the writer, after strict parsing below.
	if componentJSONExact(journal.Transaction, &tx) != nil {
		return tx, errComponentTrust
	}
	b, _ := json.Marshal(tx)
	h := sha256.Sum256(b)
	if journal.SHA256 != hex.EncodeToString(h[:]) || tx.SchemaVersion != 1 || tx.ID != id || componentRepository(tx.Component) == "" {
		return tx, errComponentTrust
	}
	if _, err = time.Parse(time.RFC3339Nano, tx.CreatedAt); err != nil {
		return tx, errComponentTrust
	}
	switch tx.State {
	case "preparing", "prepared", "switching", "committed", "rolling-back", "rolled-back":
	default:
		return tx, errComponentTrust
	}
	if err = componentValidateFiles(tx.Component, tx.Before, tx.BeforeExists); err != nil {
		return tx, err
	}
	if err = componentValidateFiles(tx.Component, tx.After, tx.AfterExists); err != nil {
		return tx, err
	}
	if tx.RollbackOf != "" {
		if !componentIDRE.MatchString(tx.RollbackOf) || tx.RollbackOf == tx.ID {
			return tx, errComponentTrust
		}
		// Rollback release identifies the original trusted download, not a newly
		// fabricated remote release for a legacy local backup.
	}
	if componentValidateRelease(tx.Release) != nil || tx.Release.Component != tx.Component {
		return tx, errComponentTrust
	}
	return tx, nil
}
func (m *componentUpdateManager) move(c, fromRelative, toRelative string, files []componentFile, exists bool) error {
	if !exists {
		return nil
	}
	from, err := m.path(fromRelative)
	if err != nil {
		return err
	}
	to, err := m.path(toRelative)
	if err != nil {
		return err
	}
	if err = m.verify(c, from, files, true); err != nil {
		return err
	}
	if _, err = os.Lstat(to); !os.IsNotExist(err) {
		return errComponentTrust
	}
	if _, err = m.mkdir(filepath.ToSlash(filepath.Dir(toRelative))); err != nil {
		return err
	}
	if err = componentCheckPath(from); err != nil {
		return err
	}
	if err = componentCheckPath(to); err != nil {
		return err
	}
	// Never overwrite destination, delete user data, terminate a process, or use
	// a delayed reboot replacement. Windows refuses a locked running executable.
	return componentRenameNoReplace(from, to)
}
func (m *componentUpdateManager) validateInstall(c string) error {
	if componentRepository(c) == "" {
		if c == "launcher" {
			return errComponentUnconfigured
		}
		return errComponentTrust
	}
	if m.isBlocked() {
		return errComponentRecovery
	}
	if err := m.installationPolicy(); err != nil {
		return err
	}
	if m.guard == nil {
		return errComponentStopped
	}
	return m.guard(c)
}
func (m *componentUpdateManager) install(ctx context.Context, c string) (err error) {
	if !m.opMu.TryLock() {
		return errComponentBusy
	}
	defer m.opMu.Unlock()
	if err = m.validateInstall(c); err != nil {
		return err
	}
	m.mu.Lock()
	download, ok := m.prepared[c]
	m.mu.Unlock()
	if !ok {
		return errors.New("请先显式检查版本并完成可信下载")
	}
	if !componentIDRE.MatchString(download.ID) || componentValidateRelease(download.Release) != nil || download.Release.Component != c {
		return errComponentTrust
	}
	manifest, err := m.path(componentDownloadRoot + "/" + download.ID + "/manifest.json")
	if err != nil {
		return err
	}
	var disk componentDownload
	if componentReadJSONStrict(manifest, &disk) != nil {
		return errComponentTrust
	}
	a, _ := json.Marshal(download)
	b, _ := json.Marshal(disk)
	if string(a) != string(b) {
		return errComponentTrust
	}
	archivePath, err := m.path(componentDownloadRoot + "/" + download.ID + "/package.zip")
	if err != nil {
		return err
	}
	hash, size, err := componentHash(archivePath)
	if err != nil || size != download.Release.Asset.Size || hash != strings.TrimPrefix(download.Release.Asset.Digest, "sha256:") {
		return errComponentTrust
	}
	source, err := m.path(componentDownloadRoot + "/" + download.ID + "/payload")
	if err != nil {
		return err
	}
	if err = m.verify(c, source, download.Files, true); err != nil {
		return err
	}
	if c == "core" {
		if err = componentValidateCore(source); err != nil {
			return err
		}
	}
	m.setProgress(c, "installing", size, size, nil)
	defer func() {
		if err != nil {
			m.setProgress(c, "failed", size, size, err)
		}
	}()
	err = m.apply(ctx, c, source, download.Files, true, download.Release, "", "")
	if err == nil {
		m.mu.Lock()
		delete(m.prepared, c)
		m.mu.Unlock()
		m.setProgress(c, "installed", size, size, nil)
	}
	return err
}
func (m *componentUpdateManager) rollback(ctx context.Context, c string) (err error) {
	if !m.opMu.TryLock() {
		return errComponentBusy
	}
	defer m.opMu.Unlock()
	if err = m.validateInstall(c); err != nil {
		return err
	}
	m.mu.Lock()
	latest, ok := m.latest[c]
	m.mu.Unlock()
	if !ok {
		return errors.New("没有可校验的组件更新备份")
	}
	// Re-read from disk; never trust stale in-memory rollback authorization.
	tx, err := m.readTransaction(latest.ID)
	if err != nil || tx.State != "committed" {
		return errComponentTrust
	}
	target, err := m.path(componentTarget(c))
	if err != nil {
		return err
	}
	if err = m.verify(c, target, tx.After, tx.AfterExists); err != nil {
		return err
	}
	source, err := m.path(componentArchiveRoot + "/" + tx.ID + "/old/payload")
	if err != nil {
		return err
	}
	if err = m.verify(c, source, tx.Before, tx.BeforeExists); err != nil {
		return err
	}
	m.setProgress(c, "rolling-back", 0, 0, nil)
	err = m.apply(ctx, c, source, tx.Before, tx.BeforeExists, tx.Release, tx.ID, tx.PreviousVersion)
	if err != nil {
		m.setProgress(c, "failed", 0, 0, err)
	} else {
		m.setProgress(c, "rolled-back", 0, 0, nil)
	}
	return err
}
func (m *componentUpdateManager) apply(ctx context.Context, c, source string, after []componentFile, afterExists bool, release componentRelease, rollbackOf, version string) (err error) {
	root, err := m.mkdir(componentArchiveRoot)
	if err != nil {
		return err
	}
	unlock, err := componentAcquireFileLock(filepath.Join(root, "update.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err = m.validateInstall(c); err != nil {
		return err
	}
	target, err := m.path(componentTarget(c))
	if err != nil {
		return err
	}
	before, beforeExists, err := m.snapshot(c, target)
	if err != nil {
		return err
	}
	if err = componentValidateFiles(c, after, afterExists); err != nil {
		return err
	}
	id, err := componentRandomID()
	if err != nil {
		return err
	}
	slot := componentArchiveRoot + "/" + id
	// A preparing journal precedes all slot payload writes. Interruption during
	// copying has no runtime mutation and recovery marks this slot rolled-back.
	if _, err = m.mkdirExclusive(slot); err != nil {
		return err
	}
	previous := m.identity(c).Version
	installedVersion := release.Version
	if rollbackOf != "" {
		installedVersion = version
	}
	tx := componentTransaction{SchemaVersion: 1, ID: id, Component: c, State: "preparing", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Release: release, Before: before, After: after, BeforeExists: beforeExists, AfterExists: afterExists, PreviousVersion: previous, RollbackOf: rollbackOf, InstalledVersion: installedVersion}
	if err = m.writeTransaction(tx); err != nil {
		m.setBlocked(true)
		return errComponentRecovery
	}
	mutated := false
	defer func() {
		if err == nil {
			return
		}
		if errors.Is(err, errComponentInterrupted) {
			m.setBlocked(true)
			return
		}
		// Restore independently of a canceled request: once mutation begins, leaving
		// the previous component available is more important than the HTTP lifetime.
		if restoreErr := m.restore(tx, mutated); restoreErr != nil {
			m.setBlocked(true)
			err = errComponentRecovery
		}
	}()
	if err = m.failpoint("preparing"); err != nil {
		return err
	}
	if _, err = m.mkdir(slot + "/new"); err != nil {
		return err
	}
	if err = m.copyPayload(c, source, slot+"/new/payload", after, afterExists); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return errors.New("组件安装已取消，未提交")
	}
	tx.State = "prepared"
	if err = m.writeTransaction(tx); err != nil {
		return err
	}
	if err = m.failpoint("prepared"); err != nil {
		return err
	}
	// Guard is repeated at the mutation boundary while Application.coreControl
	// and the installation file lock are held; no implicit disconnect is allowed.
	if err = m.validateInstall(c); err != nil {
		return err
	}
	if err = m.verify(c, target, before, beforeExists); err != nil {
		return err
	}
	tx.State = "switching"
	if err = m.writeTransaction(tx); err != nil {
		return err
	}
	mutated = true
	if err = m.move(c, componentTarget(c), slot+"/old/payload", before, beforeExists); err != nil {
		return err
	}
	if err = m.failpoint("old-moved"); err != nil {
		return err
	}
	if err = m.move(c, slot+"/new/payload", componentTarget(c), after, afterExists); err != nil {
		return err
	}
	if err = m.failpoint("new-moved"); err != nil {
		return err
	}
	if err = m.verify(c, target, after, afterExists); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return errors.New("组件安装已取消，恢复原组件")
	}
	tx.State = "committed"
	if err = m.failpoint("commit"); err != nil {
		return err
	}
	if err = m.writeTransaction(tx); err != nil {
		return err
	}
	m.mu.Lock()
	m.latest[c] = tx
	m.mu.Unlock()
	return nil
}
func (m *componentUpdateManager) restore(tx componentTransaction, mutated bool) error {
	c := tx.Component
	slot := componentArchiveRoot + "/" + tx.ID
	target, err := m.path(componentTarget(c))
	if err != nil {
		return err
	}
	current, present, err := m.snapshot(c, target)
	if err != nil {
		return err
	}
	beforeMatches := present == tx.BeforeExists && componentFilesEqual(current, tx.Before)
	if !mutated && tx.State == "preparing" {
		if !beforeMatches {
			return errComponentTrust
		}
		tx.State = "rolled-back"
		return m.writeTransaction(tx)
	}
	// If no runtime change was made, just finish the journal. This also covers
	// recovery after the old component was restored but journal flush failed.
	if beforeMatches {
		tx.State = "rolled-back"
		return m.writeTransaction(tx)
	}
	old, err := m.path(slot + "/old/payload")
	if err != nil {
		return err
	}
	if err = m.verify(c, old, tx.Before, tx.BeforeExists); err != nil {
		return err
	}
	if present && (!tx.AfterExists || !componentFilesEqual(current, tx.After)) {
		return errComponentTrust
	}
	// All identities are checked before modifying either component. A foreign
	// edit, corrupt backup, or unknown target is never deleted/overwritten.
	tx.State = "rolling-back"
	if err = m.writeTransaction(tx); err != nil {
		return err
	}
	if present {
		if err = m.move(c, componentTarget(c), slot+"/failed/payload", tx.After, true); err != nil {
			return err
		}
	}
	if err = m.move(c, slot+"/old/payload", componentTarget(c), tx.Before, tx.BeforeExists); err != nil {
		return err
	}
	if err = m.verify(c, target, tx.Before, tx.BeforeExists); err != nil {
		return err
	}
	tx.State = "rolled-back"
	return m.writeTransaction(tx)
}
func (m *componentUpdateManager) recover() (err error) {
	if !m.opMu.TryLock() {
		return errComponentBusy
	}
	defer m.opMu.Unlock()
	root, err := m.path(componentArchiveRoot)
	if err != nil {
		m.setBlocked(true)
		return errComponentRecovery
	}
	if st, e := os.Lstat(root); os.IsNotExist(e) {
		return nil
	} else if e != nil || !st.IsDir() {
		m.setBlocked(true)
		return errComponentRecovery
	}
	unlock, err := componentAcquireFileLock(filepath.Join(root, "update.lock"))
	if err != nil {
		m.setBlocked(true)
		return errComponentRecovery
	}
	defer unlock()
	defer func() { m.setBlocked(err != nil) }()
	entries, e := os.ReadDir(root)
	if e != nil || len(entries) > 4096 {
		return errComponentRecovery
	}
	all := []componentTransaction{}
	// Validate every manifest before recovering any slot. A malformed slot keeps
	// evidence and blocks startup, rather than partially applying unrelated data.
	for _, entry := range entries {
		if entry.Name() == "update.lock" {
			continue
		}
		if !entry.IsDir() || !componentIDRE.MatchString(entry.Name()) {
			return errComponentRecovery
		}
		tx, e := m.readTransaction(entry.Name())
		if e != nil {
			return errComponentRecovery
		}
		all = append(all, tx)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt == all[j].CreatedAt {
			return all[i].ID < all[j].ID
		}
		return all[i].CreatedAt < all[j].CreatedAt
	})
	pending := map[string]int{}
	for _, tx := range all {
		if tx.State != "committed" && tx.State != "rolled-back" {
			pending[tx.Component]++
		}
	}
	for _, count := range pending {
		if count > 1 {
			return errComponentRecovery
		}
	}
	for _, tx := range all {
		if tx.State != "committed" && tx.State != "rolled-back" {
			if m.guard == nil || m.guard(tx.Component) != nil {
				return errComponentRecovery
			}
			// A controlled publication introduced during an interrupted unmanaged
			// update cannot authorize mutation of its protected component paths.
			if m.installationPolicy() != nil {
				return errComponentRecovery
			}
			if e := m.restore(tx, tx.State != "preparing"); e != nil {
				return errComponentRecovery
			}
		} else if tx.State == "committed" {
			m.mu.Lock()
			m.latest[tx.Component] = tx
			m.mu.Unlock()
		}
	}
	return nil
}
