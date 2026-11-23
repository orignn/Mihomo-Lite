package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newPolicyMismatchApp(t *testing.T, subscriptionURL string) (*Application, []string) {
	t.Helper()
	a := testApplication(t)
	a.config = &Settings{ConfigName: "old"}
	a.logger = slog.New(NewLogStore(100))
	a.events = NewEventHub()
	a.proxy = httpTestProxy{}
	oldID, otherID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	oldYAML := strings.ReplaceAll(testProfileYAML, "TEST", "国外域名")
	store := profileStore{ActiveID: oldID, Profiles: []Profile{
		{ID: oldID, Name: "old", Source: "subscription", URL: subscriptionURL, UpdatedAt: "2020-01-01T00:00:00Z", Size: int64(len(oldYAML))},
		{ID: otherID, Name: "other", Source: "subscription", URL: subscriptionURL, UpdatedAt: "2020-01-01T00:00:00Z", Size: int64(len(oldYAML))},
	}}
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	custom, _ := json.Marshal([]CustomRule{
		{ID: "one", Type: "DOMAIN-SUFFIX", Value: "example.test", Policy: "国外域名", Enabled: true},
		{ID: "two", Type: "DOMAIN-SUFFIX", Value: "downloads.test", Policy: "国外域名", Enabled: true},
	})
	files := map[string][]byte{
		filepath.Join(a.dataDir, "source.yaml"):   []byte(oldYAML),
		a.customRulesPath():                       custom,
		a.pendingProfilePath():                    []byte(oldID),
		filepath.Join(a.baseDir, "launcher.json"): []byte(`{"configName":"old"}`),
		// Import checks installation before the static preflight. This stub must
		// never execute: every tested mismatch must return before core validation.
		filepath.Join(a.baseDir, "runtime", coreExecutable): []byte("synthetic non-executable core fixture"),
	}
	for _, id := range []string{oldID, otherID} {
		files[a.profileContentPath(id)] = []byte(oldYAML)
		files[a.profileContentPath(id)+".bak"] = []byte("synthetic retained backup")
		files[a.subscriptionDiffPath(id)] = []byte(`{"before":"synthetic-before","after":"synthetic-after"}`)
	}
	paths := make([]string, 0, len(files))
	for path, content := range files {
		if err := atomicWrite(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return a, paths
}

func TestCustomPolicyMismatchRejectsProfileChanges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer server.Close()
	cases := []struct {
		name   string
		update bool
		run    func(*Application) error
	}{
		{"local-add", false, func(a *Application) error { return a.addLocalProfile("candidate", testProfileYAML) }},
		{"yaml-import", false, func(a *Application) error { _, err := a.Import(strings.NewReader(testProfileYAML)); return err }},
		{"subscription-add", false, func(a *Application) error {
			return a.addSubscription(context.Background(), "candidate", server.URL, "direct")
		}},
		{"edit", false, func(a *Application) error { return a.editActiveProfile(strings.Repeat("a", 64), testProfileYAML) }},
		{"active-update", true, func(a *Application) error {
			return a.updateSubscription(context.Background(), strings.Repeat("a", 64), "direct")
		}},
		{"inactive-update", true, func(a *Application) error {
			return a.updateSubscription(context.Background(), strings.Repeat("b", 64), "direct")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, paths := newPolicyMismatchApp(t, server.URL)
			before := make(map[string][]byte)
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = data
			}
			storeBefore, _ := os.ReadFile(a.profileStorePath())
			entriesBefore, _ := os.ReadDir(a.profileDir())
			err := tc.run(a)
			if err == nil || !strings.Contains(err.Error(), "2 条") || !strings.Contains(err.Error(), "自定义规则") {
				t.Fatal("missing policy did not return the local preflight diagnostic before persistence/core validation")
			}
			for _, path := range paths {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before[path], after) {
					t.Fatal("policy rejection changed a saved profile, backup, diff, source, rules or settings")
				}
			}
			entriesAfter, _ := os.ReadDir(a.profileDir())
			if len(entriesAfter) != len(entriesBefore) {
				t.Fatal("failed import left an orphaned profile")
			}
			store, err := a.loadProfileStore()
			if err != nil || len(store.Profiles) != 2 || store.ActiveID != strings.Repeat("a", 64) {
				t.Fatal("rejection changed selection or profile count")
			}
			if !tc.update {
				after, _ := os.ReadFile(a.profileStorePath())
				if !bytes.Equal(storeBefore, after) {
					t.Fatal("rejected add/edit changed metadata")
				}
			} else {
				for _, profile := range store.Profiles {
					if profile.UpdatedAt != "2020-01-01T00:00:00Z" {
						t.Fatal("failed update changed successful update time")
					}
					if profile.LastUpdate != nil && profile.LastUpdate.Status != "failed" {
						t.Fatal("failed update reported success")
					}
				}
			}
		})
	}
}

func TestCustomPolicyMismatchAutomaticUpdatePreservesFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(testProfileYAML)) }))
	defer server.Close()
	a, paths := newPolicyMismatchApp(t, server.URL)
	store, _ := a.loadProfileStore()
	now := time.Now()
	store.Profiles[0].AutoUpdateHours = 6
	store.Profiles[0].NextUpdateAt = now.Add(-time.Hour).Format(time.RFC3339)
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	before := make(map[string][]byte)
	for _, path := range paths {
		before[path], _ = os.ReadFile(path)
	}
	a.runDueSubscriptionUpdates(context.Background(), now)
	store, err := a.loadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	update := store.Profiles[0].LastUpdate
	if update == nil || !update.Auto || update.Status != "failed" || a.operation {
		t.Fatal("automatic mismatch did not record failure and release its operation slot")
	}
	for _, path := range paths {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before[path], after) {
			t.Fatal("automatic failure changed protected fixture files")
		}
	}
}

func TestCustomPolicyDisableThenRetargetWorkaround(t *testing.T) {
	a := newProfileTestApp(t)
	custom := []CustomRule{
		{ID: "one", Type: "DOMAIN", Value: "example.test", Policy: "国外域名", Enabled: false},
		{ID: "two", Type: "DOMAIN", Value: "downloads.test", Policy: "国外域名", Enabled: false},
	}
	saved, _ := json.Marshal(custom)
	if err := atomicWrite(a.customRulesPath(), saved, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(testProfileYAML)) }))
	defer server.Close()
	if err := a.addSubscription(context.Background(), "candidate", server.URL, "direct"); err != nil {
		t.Fatal(err)
	}
	stillSaved, _ := os.ReadFile(a.customRulesPath())
	if !bytes.Equal(saved, stillSaved) {
		t.Fatal("import modified disabled custom rules")
	}
	for i := range custom {
		custom[i].Policy = "TEST"
		custom[i].Enabled = true
	}
	if err := a.saveCustomRules(context.Background(), custom); err != nil {
		t.Fatal(err)
	}
	store, _ := a.loadProfileStore()
	content, _ := os.ReadFile(a.profileContentPath(store.ActiveID))
	_, rendered, err := a.prepareConfig(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.testConfig(rendered); err != nil {
		t.Fatal(err)
	}
	if string(content) != testProfileYAML {
		t.Fatal("workaround rewrote the original subscription")
	}
}
