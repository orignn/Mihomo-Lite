//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionPolicyBackupRoundTrip(t *testing.T) {
	a, id := isolatedSubscriptionApp(t, "https://synthetic.invalid/subscription")
	store, err := a.loadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	policy := &subscriptionUpdatePolicy{Mode: "proxy", UserAgent: "synthetic-UA/1", Conditional: true, FailureBackoff: true}
	retry := &subscriptionUpdateRetry{ConsecutiveFailures: 2, LastFailureAt: at, RetryAt: at.Add(15 * time.Minute)}
	traffic := &subscriptionTraffic{Remaining: trafficInt(1536), Source: "nodes", ExpireDate: "2028-02-29", CheckedAt: at}
	p := &store.Profiles[0]
	p.UpdatePolicy = policy
	p.UpdateRetry = retry
	p.AutoUpdateHours = 6
	p.NextUpdateAt = retry.RetryAt.Format(time.RFC3339Nano)
	p.Traffic = traffic
	p.TrafficScanned = true
	content, err := os.ReadFile(a.profileContentPath(id))
	if err != nil {
		t.Fatal(err)
	}
	p.DownloadCache = &subscriptionDownloadCache{URLSHA256: sha256Hex([]byte(p.URL)), UserAgentSHA256: sha256Hex([]byte(policy.UserAgent)), ResourceSHA256: sha256Hex([]byte(p.URL)), ContentSHA256: sha256Hex(content), ETag: "\"synthetic-private-validator\""}
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	raw, err := a.exportBackup()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("downloadCache")) || bytes.Contains(raw, []byte("synthetic-private-validator")) {
		t.Fatal("private HTTP validators entered a user backup")
	}
	doc, err := decodeBackupDocument(raw)
	if err != nil {
		t.Fatal("exported policy backup failed strict verification")
	}
	if !reflect.DeepEqual(doc.Profiles[0].UpdatePolicy, policy) || !reflect.DeepEqual(doc.Profiles[0].UpdateRetry, retry) || !reflect.DeepEqual(doc.Profiles[0].Traffic, traffic) {
		t.Fatal("policy backup lost validated optional metadata")
	}
	store.Profiles[0].UpdatePolicy = &subscriptionUpdatePolicy{Mode: "direct"}
	store.Profiles[0].UpdateRetry = nil
	store.Profiles[0].Traffic = nil
	store.Profiles[0].NextUpdateAt = at.Add(6 * time.Hour).Format(time.RFC3339Nano)
	if err := a.saveProfileStore(store); err != nil {
		t.Fatal(err)
	}
	if err := a.importBackup(raw); err != nil {
		t.Fatal("isolated backup restore failed")
	}
	restored, err := a.loadProfileStore()
	if err != nil {
		t.Fatal(err)
	}
	got := restored.Profiles[0]
	if got.ID != id || !reflect.DeepEqual(got.UpdatePolicy, policy) || !reflect.DeepEqual(got.UpdateRetry, retry) || !reflect.DeepEqual(got.Traffic, traffic) || got.DownloadCache != nil || !got.TrafficScanned {
		t.Fatal("restoring policy backup lost state or restored private validators")
	}
}

func TestSubscriptionPolicyBackupRejectsInvalidMetadata(t *testing.T) {
	cases := []struct {
		name   string
		change func(*backupProfile)
	}{
		{"unknown mode", func(p *backupProfile) { p.UpdatePolicy = &subscriptionUpdatePolicy{Mode: "unexpected"} }},
		{"UA injection", func(p *backupProfile) {
			p.UpdatePolicy = &subscriptionUpdatePolicy{Mode: "direct", UserAgent: "valid\r\nX-Private: synthetic"}
		}},
		{"oversized UA", func(p *backupProfile) {
			p.UpdatePolicy = &subscriptionUpdatePolicy{Mode: "direct", UserAgent: strings.Repeat("x", 513)}
		}},
		{"negative failures", func(p *backupProfile) { p.UpdateRetry = &subscriptionUpdateRetry{ConsecutiveFailures: -1} }},
		{"unbounded failures", func(p *backupProfile) {
			p.UpdateRetry = &subscriptionUpdateRetry{ConsecutiveFailures: 1000000, LastFailureAt: time.Now().UTC(), RetryAt: time.Now().UTC()}
		}},
		{"unknown failure time", func(p *backupProfile) {
			p.UpdateRetry = &subscriptionUpdateRetry{ConsecutiveFailures: 1, RetryAt: time.Now().UTC()}
		}},
		{"retry schedule mismatch", func(p *backupProfile) {
			at := time.Now().UTC()
			p.UpdateRetry = &subscriptionUpdateRetry{ConsecutiveFailures: 1, LastFailureAt: at, RetryAt: at.Add(5 * time.Minute)}
		}},
		{"disabled schedule has retry", func(p *backupProfile) {
			at := time.Now().UTC()
			p.AutoUpdateHours = 0
			p.NextUpdateAt = ""
			p.UpdateRetry = &subscriptionUpdateRetry{ConsecutiveFailures: 1, LastFailureAt: at, RetryAt: at.Add(5 * time.Minute)}
		}},
		{"local cannot have policy", func(p *backupProfile) {
			p.Source = "local"
			p.URL = ""
			p.Traffic = nil
			p.AutoUpdateHours = 0
			p.NextUpdateAt = ""
			p.UpdatePolicy = &subscriptionUpdatePolicy{Mode: "auto", Conditional: true, FailureBackoff: true}
		}},
		{"invalid node remaining", func(p *backupProfile) {
			p.Traffic = &subscriptionTraffic{Remaining: trafficInt(-1), Source: "nodes", CheckedAt: time.Now().UTC()}
		}},
		{"invalid node expiry date", func(p *backupProfile) {
			p.Traffic = &subscriptionTraffic{ExpireDate: "2027-02-29", Source: "nodes", CheckedAt: time.Now().UTC()}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, id := isolatedSubscriptionApp(t, "https://synthetic.invalid/subscription")
			raw, err := a.exportBackup()
			if err != nil {
				t.Fatal(err)
			}
			doc, err := decodeBackupDocument(raw)
			if err != nil {
				t.Fatal(err)
			}
			before := subscriptionFixtureFiles(t, a, id)
			tc.change(&doc.Profiles[0])
			if err := a.importBackup(resignSubscriptionBackup(t, doc)); err == nil {
				t.Fatal("invalid optional policy metadata was accepted")
			}
			for path, want := range before {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatal("rejected policy backup changed saved data")
				}
			}
		})
	}
}

// Old types retain the exact pre-extension field order. Prior checksums must
// still verify without repairing or changing the original document.
type opt006LegacyTraffic struct {
	Upload    *int64    `json:"upload,omitempty"`
	Download  *int64    `json:"download,omitempty"`
	Total     *int64    `json:"total,omitempty"`
	Expire    *int64    `json:"expire,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}
type opt006LegacyProfile struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	Source          string               `json:"source"`
	URL             string               `json:"url,omitempty"`
	Traffic         *opt006LegacyTraffic `json:"traffic,omitempty"`
	CreatedAt       string               `json:"createdAt"`
	UpdatedAt       string               `json:"updatedAt"`
	Size            int64                `json:"size"`
	Active          bool                 `json:"active"`
	LastUpdate      *backupUpdate        `json:"lastUpdate,omitempty"`
	AutoUpdateHours int                  `json:"autoUpdateHours"`
	NextUpdateAt    string               `json:"nextUpdateAt,omitempty"`
	YAML            string               `json:"yaml"`
	SHA256          string               `json:"sha256"`
	PreviousYAML    string               `json:"previousYaml,omitempty"`
	PreviousSHA256  string               `json:"previousSha256,omitempty"`
}
type opt006LegacyDocument struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Version       string                `json:"version"`
	ExportedAt    string                `json:"exportedAt"`
	Scope         string                `json:"scope"`
	ActiveID      string                `json:"activeId"`
	Profiles      []opt006LegacyProfile `json:"profiles"`
	Rules         []CustomRule          `json:"rules"`
	Checksum      string                `json:"checksum,omitempty"`
}

func TestSubscriptionPolicyLegacyBackupCanonicalChecksum(t *testing.T) {
	for _, withTraffic := range []bool{false, true} {
		t.Run(map[bool]string{false: "v1 without quota", true: "v1 header quota"}[withTraffic], func(t *testing.T) {
			id := strings.Repeat("a", 64)
			at := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
			p := opt006LegacyProfile{ID: id, Name: "Synthetic legacy", Source: "subscription", URL: "https://synthetic.invalid/subscription", CreatedAt: at.Format(time.RFC3339), UpdatedAt: at.Format(time.RFC3339), Size: int64(len(testProfileYAML)), Active: true, YAML: testProfileYAML, SHA256: sha256Hex([]byte(testProfileYAML))}
			if withTraffic {
				p.Traffic = &opt006LegacyTraffic{Upload: trafficInt(1), Download: trafficInt(2), Total: trafficInt(100), CheckedAt: at}
			}
			doc := opt006LegacyDocument{SchemaVersion: 1, Version: "0.6.13", ExportedAt: at.Format(time.RFC3339Nano), Scope: backupScope, ActiveID: id, Profiles: []opt006LegacyProfile{p}, Rules: []CustomRule{}}
			canonical, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			doc.Checksum = sha256Hex(canonical)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeBackupDocument(raw)
			if err != nil {
				t.Fatal("pre-extension canonical checksum no longer verifies")
			}
			if decoded.Profiles[0].UpdatePolicy != nil || decoded.Profiles[0].UpdateRetry != nil {
				t.Fatal("legacy backup invented an explicit policy or retry")
			}
			if _, _, _, err := validateBackupProfile(decoded.Profiles[0], id); err != nil {
				t.Fatal("pre-extension profile no longer passes validation")
			}
		})
	}
}
