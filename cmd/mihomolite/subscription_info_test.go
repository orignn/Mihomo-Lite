package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func trafficInt(value int64) *int64 { return &value }

func TestSubscriptionUserinfoConservativeFields(t *testing.T) {
	for _, tc := range []struct {
		name, raw                       string
		upload, download, total, expire *int64
	}{
		{"absent", "", nil, nil, nil, nil},
		{"unknown only", "plan=opaque", nil, nil, nil, nil},
		{"complete", "UPLOAD=100; download=200; total=1000; expire=1900000000", trafficInt(100), trafficInt(200), trafficInt(1000), trafficInt(1900000000)},
		{"zero is not invented unlimited", "upload=0;download=0;total=0;expire=0", trafficInt(0), trafficInt(0), trafficInt(0), trafficInt(0)},
		{"partial", "upload=5;total=10", trafficInt(5), nil, trafficInt(10), nil},
		{"negative", "upload=-1;download=3;total=-5", nil, trafficInt(3), nil, nil},
		{"float", "upload=1.5;download=3;total=10", nil, trafficInt(3), trafficInt(10), nil},
		{"plus is malformed", "upload=+1", nil, nil, nil, nil},
		{"exponent is malformed", "upload=1e3;download=2", nil, trafficInt(2), nil, nil},
		{"comma is malformed", "total=1,024;expire=1900000000", nil, nil, nil, trafficInt(1900000000)},
		{"empty known field", "upload=;download=2", nil, trafficInt(2), nil, nil},
		{"malformed known separator", "total:10;upload=2", trafficInt(2), nil, nil, nil},
		{"malformed known whitespace", "total 10;upload=2", trafficInt(2), nil, nil, nil},
		{"malformed separator duplicate", "total=10;TOTAL:10;upload=2", trafficInt(2), nil, nil, nil},
		{"third duplicate cannot revive field", "total=1;total=-1;TOTAL=1;upload=2", trafficInt(2), nil, nil, nil},
		{"conflicting duplicate", "upload=5;total=100;TOTAL=200", trafficInt(5), nil, nil, nil},
		{"invalid duplicate", "upload=5;total=100;total=-1", trafficInt(5), nil, nil, nil},
		{"identical duplicate", "total=100;total=100", nil, nil, nil, nil},
		{"malformed first duplicate", "total;total=100", nil, nil, nil, nil},
		{"safe integer boundary", "total=9007199254740991", nil, nil, trafficInt(maxSubscriptionTrafficValue), nil},
		{"unsafe integer", "total=9007199254740992", nil, nil, nil, nil},
		{"int64 overflow", "total=9223372036854775808", nil, nil, nil, nil},
		{"expiry boundary", "expire=253402300799", nil, nil, nil, trafficInt(maxSubscriptionExpiry)},
		{"expiry overflow", "expire=253402300800", nil, nil, nil, nil},
		{"oversize metadata", strings.Repeat("x", 8193), nil, nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSubscriptionUserinfo(tc.raw)
			if tc.upload == nil && tc.download == nil && tc.total == nil && tc.expire == nil {
				if got != nil {
					t.Fatal("all malformed or absent fields must remain unknown")
				}
				return
			}
			if got == nil || !reflect.DeepEqual(got.Upload, tc.upload) || !reflect.DeepEqual(got.Download, tc.download) || !reflect.DeepEqual(got.Total, tc.total) || !reflect.DeepEqual(got.Expire, tc.expire) || !validSubscriptionTraffic(got) {
				t.Fatalf("parsed fields do not match expected optional values: %+v", got)
			}
			if got.Source != "header" || got.Remaining != nil || got.Used != nil || got.ExpireDate != "" {
				t.Fatal("strict header parsing invented node-only fields or lost provenance")
			}
		})
	}
}

func TestSubscriptionDownloadReadsAllUserinfoHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Subscription-Userinfo", "upload=1;download=2;total=10")
		w.Header().Add("Subscription-Userinfo", "TOTAL=20")
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer server.Close()
	a := testApplication(t)
	got, err := a.downloadSubscriptionWithInfo(context.Background(), server.URL, "direct")
	if err != nil || got.Traffic == nil || got.Traffic.Total != nil || got.Traffic.Upload == nil || *got.Traffic.Upload != 1 {
		t.Fatal("multiple headers were not handled conservatively")
	}
}

func TestSubscriptionDownloadNodeInfo200Only(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/reject" {
			w.WriteHeader(http.StatusForbidden)
		}
		_, _ = w.Write(nodeInfoYAML("剩余流量：123.45 GB", "套餐到期：2028-02-29"))
	}))
	defer server.Close()
	a := testApplication(t)
	got, err := a.downloadSubscriptionWithInfo(context.Background(), server.URL+"/ok", "direct")
	if err != nil || got.Traffic == nil || got.Traffic.Remaining == nil || *got.Traffic.Remaining != 12345*(1<<30)/100 || got.Traffic.Source != "nodes" || got.Traffic.ExpireDate != "2028-02-29" {
		t.Fatal("HTTP 200 response did not expose synthetic node metadata")
	}
	if got.Traffic.Upload != nil || got.Traffic.Download != nil || got.Traffic.Used != nil || got.Traffic.Total != nil || got.Traffic.Expire != nil {
		t.Fatal("remaining-only HTTP node hints invented quota fields or an expiry instant")
	}
	if rejected, err := a.downloadSubscriptionWithInfo(context.Background(), server.URL+"/reject", "direct"); err == nil || rejected.Traffic != nil {
		t.Fatal("failed HTTP response supplied node metadata")
	}
}

func TestSubscriptionDownloadNodeInfoCannotRepairDuplicateHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Subscription-Userinfo", "upload=1;download=2;total=10")
		w.Header().Add("Subscription-Userinfo", "TOTAL=20")
		_, _ = w.Write(nodeInfoYAML("Used:3 B", "Remaining:97 B", "Total:100 B"))
	}))
	defer server.Close()
	a := testApplication(t)
	got, err := a.downloadSubscriptionWithInfo(context.Background(), server.URL, "direct")
	if err != nil || got.Traffic == nil || got.Traffic.Upload == nil || *got.Traffic.Upload != 1 || got.Traffic.Download == nil || *got.Traffic.Download != 2 {
		t.Fatal("valid header legs were lost")
	}
	if got.Traffic.Total != nil || got.Traffic.Used != nil || got.Traffic.Remaining != nil || got.Traffic.Source != "header" {
		t.Fatal("node hints repaired a duplicate header field directly or indirectly")
	}
}

func TestSubscriptionRedirectDoesNotSendTokenAsReferer(t *testing.T) {
	var referer string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer = r.Header.Get("Referer")
		_, _ = w.Write([]byte(testProfileYAML))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/final", http.StatusFound)
	}))
	defer source.Close()
	a := testApplication(t)
	if _, err := a.downloadSubscriptionWithInfo(context.Background(), source.URL+"/?token=synthetic-private", "direct"); err != nil {
		t.Fatal("isolated redirect failed")
	}
	if referer != "" {
		t.Fatal("subscription redirect sent a Referer")
	}
}

func TestSubscriptionRedirectRejectsUserinfoAndHTTPSDowngrade(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unsafe redirect reached its destination") }))
	defer target.Close()
	for _, tls := range []bool{false, true} {
		t.Run(map[bool]string{false: "userinfo", true: "HTTPS downgrade"}[tls], func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destination := target.URL
				if !tls {
					destination = strings.Replace(destination, "http://", "http://synthetic:private@", 1)
				}
				http.Redirect(w, r, destination, http.StatusFound)
			})
			var source *httptest.Server
			if tls {
				source = httptest.NewTLSServer(handler)
				original := http.DefaultTransport
				http.DefaultTransport = source.Client().Transport
				t.Cleanup(func() { http.DefaultTransport = original })
			} else {
				source = httptest.NewServer(handler)
			}
			defer source.Close()
			a := testApplication(t)
			_, err := a.downloadSubscriptionWithInfo(context.Background(), source.URL+"/?token=synthetic-private", "direct")
			if err == nil || strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), source.URL) {
				t.Fatal("unsafe redirect was accepted or its error leaked subscription data")
			}
		})
	}
}

func TestSubscriptionValidationSafeErrorKeepsCompatibilityDiagnostic(t *testing.T) {
	secret := safeSubscriptionValidationError(assertSubscriptionError("synthetic-private-url token=synthetic-private password=synthetic-private"))
	if strings.Contains(secret.Error(), "synthetic-private") {
		t.Fatal("unsafe core output escaped the validation boundary")
	}
	compatible := &customPolicyCompatibilityError{message: "有 2 条启用的自定义规则引用了缺失策略"}
	if safeSubscriptionValidationError(compatible) != compatible {
		t.Fatal("safe existing policy guidance was lost")
	}
}

type assertSubscriptionError string

func (e assertSubscriptionError) Error() string { return string(e) }

func resignSubscriptionBackup(t *testing.T, doc backupDocument) []byte {
	t.Helper()
	doc.Checksum = ""
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Checksum = sha256Hex(raw)
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
