package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEventHubSlowClientKeepsLatestSettings(t *testing.T) {
	h := NewEventHub()
	ch := make(chan State, 1)
	h.clients[1] = ch
	for i := 0; i < 500; i++ {
		h.Publish(State{PID: i, RuntimeSettings: RuntimeSettings{CloseToTray: i%2 == 0}})
	}
	got := <-ch
	if got.PID != 499 || got.CloseToTray {
		t.Fatalf("slow client retained stale snapshot: %+v", got)
	}
	if len(ch) != 0 {
		t.Fatal("stale snapshots queued after latest")
	}
}

func TestEventHubInitialSubscriptionDoesNotLoseSettings(t *testing.T) {
	h := NewEventHub()
	entered := make(chan struct{})
	release := make(chan struct{})
	h.current = func() State {
		close(entered)
		<-release
		return State{RuntimeSettings: RuntimeSettings{CloseToTray: false}}
	}
	server := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	defer server.Close()
	type result struct {
		resp *http.Response
		err  error
	}
	responses := make(chan result, 1)
	go func() { r, e := server.Client().Get(server.URL); responses <- result{r, e} }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("current snapshot not requested")
	}
	h.Publish(State{Version: "newest", RuntimeSettings: RuntimeSettings{AutoStart: true, CloseToTray: true}})
	close(release)
	var r result
	select {
	case r = <-responses:
	case <-time.After(3 * time.Second):
		t.Fatal("initial SSE response not flushed")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer r.resp.Body.Close()
	scanner := bufio.NewScanner(r.resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var got State
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &got); err != nil {
			t.Fatal(err)
		}
		if got.Version != "newest" || !got.AutoStart || !got.CloseToTray {
			t.Fatalf("subscription gap lost latest state: %+v", got)
		}
		return
	}
	t.Fatal("missing initial SSE state")
}
