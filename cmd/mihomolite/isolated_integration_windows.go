//go:build windows && isolatedtest

package main

import (
	"errors"
	"golang.org/x/sys/windows/registry"
)

// Never included in a release. Use the real WindowsProxy snapshot/rollback
// implementation with private HKCU data and no system-wide WinINet changes.
func init() {
	autoRunKey = isolatedRegistryBase() + "\\Run"
	newApplicationProxy = func() systemProxy { return &WindowsProxy{backend: isolatedProxyBackend{}} }
}

func isolatedRegistryBase() string      { return "Software\\MihomoLite\\Tests\\" + instanceNamespace }
func isolatedProxyRegistryPath() string { return isolatedRegistryBase() + "\\Proxy" }

type isolatedProxyBackend struct{}

func (isolatedProxyBackend) Read() (proxySnapshot, error) {
	s, err := readProxySnapshotAt(isolatedProxyRegistryPath())
	if errors.Is(err, registry.ErrNotExist) {
		return proxySnapshot{}, nil
	}
	return s, err
}
func (isolatedProxyBackend) Write(s proxySnapshot) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, isolatedProxyRegistryPath(), registry.SET_VALUE)
	if err != nil {
		return err
	}
	key.Close()
	return writeProxyRegistryAt(isolatedProxyRegistryPath(), s)
}
func (isolatedProxyBackend) Apply(string, string) error { return nil }
