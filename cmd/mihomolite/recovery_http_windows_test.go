//go:build windows

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoveryHTTPRequiresExplicitConfirmation(t *testing.T) {
	for _, path := range []string{"/api/recovery/keep-current-proxy", "/api/recovery/stop-leftover-core"} {
		for _, body := range []string{`{}`, `{`, `{"confirm":false}`, `{"confirm":"true"}`} {
			t.Run(path+body, func(t *testing.T) {
				a := httpTestApplication(t)
				r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17653"+path, strings.NewReader(body))
				r.Header.Set("X-Mihomo-Lite", "1")
				w := httptest.NewRecorder()
				a.Routes().ServeHTTP(w, r)
				if w.Code != http.StatusBadRequest {
					t.Fatal("unconfirmed request accepted", w.Code)
				}
				if a.operation {
					t.Fatal("unconfirmed request started an operation")
				}
			})
		}
	}
}
