//go:build opt009

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in official-core syntax validation only. No Application.Start, network
// access, system proxy, providers, Geo rules, or non-test input is involved.
func TestOPT009OfficialCoreValidation(t *testing.T) {
	core := os.Getenv("MIHOMOLITE_OPT009_TEST_CORE")
	if core == "" {
		t.Fatal("explicit isolated official core path required")
	}
	root := filepath.Join(mustGetwd(t), ".cache", "tests", "opt009")
	absolute, err := filepath.Abs(core)
	if err != nil {
		t.Fatal("invalid core path")
	}
	rel, err := filepath.Rel(root, absolute)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatal("core must be a copy inside opt009 test cache")
	}
	// Reject all symlink/reparse ancestors, not just the executable itself.
	for cursor := absolute; ; cursor = filepath.Dir(cursor) {
		info, err := os.Lstat(cursor)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("core path is missing or redirects outside isolation")
		}
		if cursor == root {
			break
		}
	}
	run, err := os.MkdirTemp(root, "native-")
	if err != nil {
		t.Fatal("isolated native directory creation failed")
	}
	t.Log("synthetic native evidence directory: " + run)
	cases := opt009ValidCases()
	var mixed []string
	for _, tc := range cases {
		mixed = append(mixed, tc.link)
	}
	cases = append(cases, opt009Case{"mixed-protocols", strings.Join(mixed, "\n"), "mixed"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := convertShareLinkSubscription([]byte(tc.link), tc.protocol)
			if err != nil {
				t.Fatalf("synthetic conversion failed: %v", err)
			}
			// No mixed-port/controller means -t cannot produce active listeners; no
			// Geo/provider references can cause database/subscription downloads.
			data = append([]byte("dns: {enable: false}\ntun: {enable: false}\nallow-lan: false\nbind-address: 127.0.0.1\n"), data...)
			dir := filepath.Join(run, tc.name)
			if os.Mkdir(dir, 0700) != nil {
				t.Fatal("case directory creation failed")
			}
			config := filepath.Join(dir, "config.yaml")
			if os.WriteFile(config, data, 0600) != nil {
				t.Fatal("synthetic config write failed")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, absolute, "-t", "-d", dir, "-f", config)
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if os.WriteFile(filepath.Join(dir, "core-test.log"), output, 0600) != nil {
				t.Fatal("native log write failed")
			}
			if err != nil {
				t.Fatalf("official core -t rejected synthetic fixture: %s", output)
			}
		})
	}
}
