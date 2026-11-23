//go:build windows

package main

import (
	"os"
	"testing"
)

func TestWindowsProxyRoundTrip(t *testing.T) {
	if os.Getenv("MIHOMOLITE_TEST_SYSTEM_PROXY") != "1" {
		t.Skip("set MIHOMOLITE_TEST_SYSTEM_PROXY=1 to run Windows integration test")
	}
	before, err := readProxySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer restoreProxy(before)
	proxy := NewWindowsProxy()
	server := "127.0.0.1:54321"
	bypass := "<local>;localhost;127.*"
	if err := proxy.Enable(server, bypass); err != nil {
		t.Fatal(err)
	}
	afterEnable, err := readProxySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if afterEnable.enabled != 1 || afterEnable.server != server || afterEnable.bypass != bypass {
		t.Fatalf("not enabled: %+v", afterEnable)
	}
	if err := proxy.Disable(); err != nil {
		t.Fatal(err)
	}
	afterDisable, err := readProxySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if afterDisable != before {
		t.Fatalf("not restored: before=%+v after=%+v", before, afterDisable)
	}
}
