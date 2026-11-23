//go:build windows

package main

import (
	"runtime"
	"testing"
	"time"

	"github.com/getlantern/systray"
)

func TestTrayClickChannelsRetainCommandsWhileBusy(t *testing.T) {
	item := &systray.MenuItem{ClickedCh: make(chan struct{})}
	bufferTrayClicks(item)
	// This is systray's non-blocking publication pattern while no receiver is
	// ready. The command must remain pending for the later tray-loop receive.
	select {
	case item.ClickedCh <- struct{}{}:
	default:
		t.Fatal("click was dropped while the tray loop was busy")
	}
	select {
	case <-item.ClickedCh:
	case <-time.After(time.Second):
		t.Fatal("pending click was not delivered to the tray loop")
	}
}

func TestTrayClickChannelsRetainAllMenuActions(t *testing.T) {
	for range 9 {
		item := &systray.MenuItem{ClickedCh: make(chan struct{})}
		bufferTrayClicks(item)
		runtime.Gosched() // Publish while no receiver is waiting.
		select {
		case item.ClickedCh <- struct{}{}:
		default:
			t.Fatal("menu action was dropped while the tray loop was busy")
		}
		<-item.ClickedCh
	}
}
