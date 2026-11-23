package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var nodeInfoCheckedAt = time.Date(2026, time.November, 22, 5, 0, 0, 0, time.UTC)

func nodeInfoInt(value int64) *int64 { return &value }

// Every fixture is synthetic; no actual subscription or provider is read.
func nodeInfoYAML(names ...string) []byte {
	var content strings.Builder
	content.WriteString("proxies:\n")
	for _, name := range names {
		quoted, _ := json.Marshal(name)
		content.WriteString("  - name: " + string(quoted) + "\n    type: socks5\n    server: synthetic.invalid\n    port: 1080\n")
	}
	return []byte(content.String())
}

func assertNodeTraffic(t *testing.T, got, want *subscriptionTraffic) {
	t.Helper()
	if want != nil {
		want.CheckedAt = nodeInfoCheckedAt
		if want.Source == "" {
			want.Source = "nodes"
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("optional metadata differs: got=%+v want=%+v", got, want)
	}
	if got != nil && !validSubscriptionTraffic(got) {
		t.Fatal("parser returned invalid metadata")
	}
}

func TestSubscriptionNodeInfoLabelsAndUnits(t *testing.T) {
	labels := [][]string{
		{"剩余流量", "剩余 流量", "流量剩余", "剩余", "可用流量", "Remaining", "Remaining Traffic", "Data Left", "Traffic Remaining", "Available Quota"},
		{"已用流量", "已使用流量", "流量已用", "已消耗流量", "Used", "Used Data", "Data Usage", "Consumed Traffic"},
		{"总流量", "总额", "流量总量", "套餐流量", "总计流量", "Total", "Total Data", "Quota Total"},
	}
	units := []struct {
		name  string
		bytes int64
	}{
		{"B", 1}, {"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30}, {"TB", 1 << 40},
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
	}
	for kind, group := range labels {
		for _, label := range group {
			for _, unit := range units {
				t.Run(fmt.Sprintf("%d/%s/%s", kind, label, unit.name), func(t *testing.T) {
					got := parseSubscriptionNodeInfo(nodeInfoYAML("📊 "+label+" ： 💾 1.25 "+unit.name+" 🟢 | SYN-HK 2026-11-22 [2x]"), nodeInfoCheckedAt)
					want := &subscriptionTraffic{}
					value := nodeInfoInt(unit.bytes * 5 / 4) // rational conversion, flooring a fractional byte
					switch kind {
					case 0:
						want.Remaining = value
					case 1:
						want.Used = value
					case 2:
						want.Total = value
					}
					assertNodeTraffic(t, got, want)
				})
			}
		}
	}
}

func TestSubscriptionNodeInfoExactDecimalBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value int64
	}{
		{"剩余流量：123.45 GB", 12345 * (1 << 30) / 100},
		{"Remaining: 123.45 GiB", 12345 * (1 << 30) / 100},
		{"Remaining: 1.25 gb", 5 * (1 << 30) / 4},
		{"Remaining: 1.25 gib", 5 * (1 << 30) / 4},
		{"Remaining: 001.2500 GB", 5 * (1 << 30) / 4},
		{"Remaining: 0 B", 0},
		{"Remaining: 0.0000000001 GB", 0},
		{"Remaining: 9007199254740991 B", maxSubscriptionTrafficValue},
		{"Remaining: 9007199254740990.999999999999 B", maxSubscriptionTrafficValue - 1},
		{"Remaining: 8191.9999999999990905052982270717620849609375 TiB", maxSubscriptionTrafficValue},
		{"Remaining: 8\u00a0GiB", 8 * (1 << 30)},
		{"Remaining\t=\t8GiB", 8 * (1 << 30)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertNodeTraffic(t, parseSubscriptionNodeInfo(nodeInfoYAML(tc.name), nodeInfoCheckedAt), &subscriptionTraffic{Remaining: nodeInfoInt(tc.value)})
		})
	}
}

func TestSubscriptionNodeInfoRejectsMalformedBytes(t *testing.T) {
	values := []string{
		"-1 GB", "+1 GB", "−1 GB", "－1 GB", "➖1GB", "➕1GB", "✕2GB", "✖2GB",
		"1e3 GB", "1E+3GB", "1,5GB", "1,024 GB", "1，5GB", "1 024 GB", "1.234.5 GB", "1..0GB", ".5GB", "1.GB",
		"NaN GB", "Infinity GB", "1 PB", "1mbps", "1 GBps", "1 GB/s", "1 GB / s", "1GB2", "1GB_2",
		"1GB,5MB", "1GB-2GB", "1GB – 2GB", "1GB‐2GB", "1GB➖2GB", "1GB 2GB", "1GB per second", "1GB 每秒", "1MiB/2x", "9007199254740992 B", "9223372036854775808 B",
		"8192 TiB", "8192 TB", "8388608 GiB", "9007199254740991.01 B",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			assertNodeTraffic(t, parseSubscriptionNodeInfo(nodeInfoYAML("剩余流量："+value), nodeInfoCheckedAt), nil)
		})
	}
	for _, name := range []string{
		"SYN-HK 2027-02-28 [2x]", "SYN-HK 123GB", "剩余5天", "Remaining 5 days", "Used 5x", "总流量：2倍",
		"Remaining5 GB", "SYN-Unused: 1GB", "Totality: 1GB", "Total Recall 2026", "upload:1GB download:2GB",
	} {
		t.Run(name, func(t *testing.T) {
			assertNodeTraffic(t, parseSubscriptionNodeInfo(nodeInfoYAML(name), nodeInfoCheckedAt), nil)
		})
	}
}

func TestSubscriptionNodeInfoExactDatesOnly(t *testing.T) {
	for _, tc := range []struct{ name, date string }{
		{"到期时间：2028-02-29", "2028-02-29"},
		{"套餐到期：2027/5/6", "2027-05-06"},
		{"过期日期：2027.05.06", "2027-05-06"},
		{"有效期至：2027年5月6日", "2027-05-06"},
		{"Expiry Date: 2028.2.29", "2028-02-29"},
		{"Expires on: 2027-5-6", "2027-05-06"},
		{"Expiration date: 2027/05/06", "2027-05-06"},
		{"Valid until: 2027年5月6日", "2027-05-06"},
		{"📅 Expire Date：9999-12-31", "9999-12-31"},
		{"到期日：0001-01-01", "0001-01-01"},
		{"到期时间：2027-01-02 12:34:56", "2027-01-02"}, // date known; precise instant/time zone unknown
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertNodeTraffic(t, parseSubscriptionNodeInfo(nodeInfoYAML(tc.name), nodeInfoCheckedAt), &subscriptionTraffic{ExpireDate: tc.date})
		})
	}
	for _, name := range []string{
		"到期：2027-02-29", "到期：2026-04-31", "到期：2027-00-01", "到期：2027-13-01", "到期：2027-01-00",
		"到期：0000-01-01", "到期：10000-01-01", "Expire: 27-01-01", "Expire: 2027/01-01", "Expire: 2027-01-011",
		"Expire: 2027-01-01-2028-01-01", "Expire: 2027-01-01 to 2028-01-01", "到期：2027-01-01至2028-01-01",
		"有效期：剩余30天", "Expires in 5 days", "到期时间：1900000000", "Expire: unlimited", "2027-01-01",
	} {
		t.Run(name, func(t *testing.T) {
			assertNodeTraffic(t, parseSubscriptionNodeInfo(nodeInfoYAML(name), nodeInfoCheckedAt), nil)
		})
	}
}

func TestSubscriptionNodeInfoIndependentValuesAndConflicts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		want  *subscriptionTraffic
	}{
		{"remaining only", []string{"剩余流量：90 B"}, &subscriptionTraffic{Remaining: nodeInfoInt(90)}},
		{"used only", []string{"已用流量：10 B"}, &subscriptionTraffic{Used: nodeInfoInt(10)}},
		{"total only", []string{"总流量：100 B"}, &subscriptionTraffic{Total: nodeInfoInt(100)}},
		{"remaining and total do not invent used", []string{"剩余流量：90 B", "总流量：100 B"}, &subscriptionTraffic{Remaining: nodeInfoInt(90), Total: nodeInfoInt(100)}},
		{"used and total do not invent remaining", []string{"已用流量：10 B", "总流量：100 B"}, &subscriptionTraffic{Used: nodeInfoInt(10), Total: nodeInfoInt(100)}},
		{"used and remaining do not invent total", []string{"已用流量：10 B", "剩余流量：90 B"}, &subscriptionTraffic{Used: nodeInfoInt(10), Remaining: nodeInfoInt(90)}},
		{"all values in one name", []string{"Remaining:60 GB, Used:40 GB, Total:100 GB | Expires:2028-02-29"}, &subscriptionTraffic{Remaining: nodeInfoInt(60 << 30), Used: nodeInfoInt(40 << 30), Total: nodeInfoInt(100 << 30), ExpireDate: "2028-02-29"}},
		{"equal values and units merge", []string{"剩余流量：1 GB", "Remaining:1024 MB", "Remaining:1 GiB"}, &subscriptionTraffic{Remaining: nodeInfoInt(1 << 30)}},
		{"equal dates merge", []string{"到期：2028-02-29", "Expires:2028年2月29日"}, &subscriptionTraffic{ExpireDate: "2028-02-29"}},
		{"remaining conflict", []string{"Remaining:1 GB", "Remaining:2 GB"}, nil},
		{"same name conflict", []string{"Remaining:1 GB | Remaining:2 GB"}, nil},
		{"other optional fields survive remaining conflict", []string{"Remaining:1 GB", "Remaining:2 GB", "Total:10 GB"}, &subscriptionTraffic{Total: nodeInfoInt(10 << 30)}},
		{"malformed hint cannot be repaired", []string{"Remaining:-1 GB", "Remaining:1 GB"}, nil},
		{"overflow hint cannot be repaired", []string{"Remaining:9007199254740992 B", "Remaining:1 B"}, nil},
		{"unsupported byte unit cannot be repaired", []string{"Remaining:1 PB", "Remaining:1 GB"}, nil},
		{"date conflict leaves remaining", []string{"Remaining:1 GB", "Expires:2028-02-29", "Expires:2028-03-01"}, &subscriptionTraffic{Remaining: nodeInfoInt(1 << 30)}},
		{"invalid date cannot be repaired", []string{"Expires:2027-02-29", "Expires:2028-02-29"}, nil},
		{"remaining over total conflicts", []string{"Remaining:120 B", "Total:100 B"}, nil},
		{"used over total conflicts", []string{"Used:120 B", "Total:100 B"}, nil},
		{"inconsistent sum conflicts", []string{"Used:10 B", "Remaining:90 B", "Total:1010 B"}, nil},
		{"date survives inconsistent quota", []string{"Used:10 B", "Remaining:90 B", "Total:1010 B", "Expires:2028-02-29"}, &subscriptionTraffic{ExpireDate: "2028-02-29"}},
		{"one subbyte flooring difference", []string{"Used:99.99 GB", "Remaining:0.01 GB", "Total:100 GB"}, &subscriptionTraffic{Used: nodeInfoInt(9999 * (1 << 30) / 100), Remaining: nodeInfoInt((1 << 30) / 100), Total: nodeInfoInt(100 << 30)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertNodeTraffic(t, parseSubscriptionNodeInfo(nodeInfoYAML(tc.names...), nodeInfoCheckedAt), tc.want)
		})
	}
}

func TestSubscriptionNodeInfoYAMLProjectionOnly(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          *subscriptionTraffic
	}{
		{"server text ignored", "proxies: [{name: SYN-A, server: 'Remaining:1 GB', url: 'Expires:2028-02-29'}]", nil},
		{"nested name ignored", "proxies: [{name: SYN-A, options: {name: 'Remaining:1 GB'}}]", nil},
		{"root name ignored", "name: 'Remaining:1 GB'\nproxies: [{name: SYN-A}]", nil},
		{"rules and comments ignored", "# Remaining:1 GB\nproxies: [{name: SYN-A}]\nrules: ['Remaining:1 GB']", nil},
		{"providers never fetched", "proxy-providers: {SYN: {type: http, url: 'https://synthetic.invalid/Remaining:1 GB'}}", nil},
		{"provider name ignored", "proxy-providers: {SYN: {name: 'Remaining:1 GB'}}\nproxies: [{name: SYN-A}]", nil},
		{"provider payload ignored", "proxy-providers: {SYN: {payload: [{name: 'Remaining:1 GB'}]}}\nproxies: [{name: SYN-A}]", nil},
		{"block scalar name", "proxies:\n  - name: |\n      剩余流量：1 GB\n", &subscriptionTraffic{Remaining: nodeInfoInt(1 << 30)}},
		{"explicit string scalar", "proxies: [{name: !!str '剩余流量：1 GB'}]", &subscriptionTraffic{Remaining: nodeInfoInt(1 << 30)}},
		{"direct anchor does not need expansion", "proxies: [{name: &SYN 'Remaining:1 GB'}]", &subscriptionTraffic{Remaining: nodeInfoInt(1 << 30)}},
		{"unrelated server alias ignored", "x: &SYN 'Remaining:2 GB'\nproxies: [{name: 'Remaining:1 GB', server: *SYN}]", &subscriptionTraffic{Remaining: nodeInfoInt(1 << 30)}},
		{"unrelated alias cycle not expanded", "x: &SYN {next: *SYN}\nproxies: [{name: 'Remaining:1 GB'}]", &subscriptionTraffic{Remaining: nodeInfoInt(1 << 30)}},
		{"sequence root", "- name: 'Remaining:1 GB'", nil},
		{"scalar proxies", "proxies: 'Remaining:1 GB'", nil},
		{"map proxies", "proxies: {name: 'Remaining:1 GB'}", nil},
		{"uppercase root key not guessed", "Proxies: [{name: 'Remaining:1 GB'}]", nil},
		{"duplicate proxies", "proxies: [{name: 'Remaining:1 GB'}]\nproxies: [{name: 'Remaining:1 GB'}]", nil},
		{"duplicate name even identical", "proxies: [{name: 'Remaining:1 GB', name: 'Remaining:1 GB'}]", nil},
		{"duplicate unrelated proxy key", "proxies: [{name: 'Remaining:1 GB', server: SYN-A, server: SYN-B}]", nil},
		{"duplicate unrelated root key", "x: SYN-A\nx: SYN-B\nproxies: [{name: 'Remaining:1 GB'}]", nil},
		{"root merge hides declarations", "x: &SYN {proxies: []}\n<<: *SYN\nproxies: [{name: 'Remaining:1 GB'}]", nil},
		{"quoted merge key conservatively rejected", "'<<': {proxies: []}\nproxies: [{name: 'Remaining:1 GB'}]", nil},
		{"proxy merge cannot be skipped", "x: &SYN {name: 'Remaining:2 GB'}\nproxies: [{<<: *SYN, name: 'Remaining:1 GB'}]", nil},
		{"proxies alias", "x: &SYN [{name: 'Remaining:1 GB'}]\nproxies: *SYN", nil},
		{"proxy mapping alias", "x: &SYN {name: 'Remaining:1 GB'}\nproxies: [*SYN]", nil},
		{"name alias", "x: &SYN 'Remaining:1 GB'\nproxies: [{name: *SYN}]", nil},
		{"alias cannot hide another conflicting hint", "x: &SYN {name: 'Remaining:2 GB'}\nproxies: [{name: 'Remaining:1 GB'}, *SYN]", nil},
		{"complex name", "proxies: [{name: {name: 'Remaining:1 GB'}}]", nil},
		{"nonstring name", "proxies: [{name: 123}, {name: 'Remaining:1 GB'}]", nil},
		{"custom tag name", "proxies: [{name: !synthetic 'Remaining:1 GB'}]", nil},
		{"missing name does not expose server", "proxies: [{server: 'Remaining:2 GB'}, {name: 'Remaining:1 GB'}]", &subscriptionTraffic{Remaining: nodeInfoInt(1 << 30)}},
		{"malformed YAML", "proxies: [{name: 'Remaining:1 GB'}", nil},
		{"multiple documents", "proxies: [{name: 'Remaining:1 GB'}]\n---\nproxies: []", nil},
		{"second empty document", "proxies: [{name: 'Remaining:1 GB'}]\n---\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte(tc.content)
			before := append([]byte(nil), content...)
			assertNodeTraffic(t, parseSubscriptionNodeInfo(content, nodeInfoCheckedAt), tc.want)
			if !bytes.Equal(content, before) {
				t.Fatal("node hint parsing modified YAML input")
			}
		})
	}
}

func TestSubscriptionNodeInfoBoundsTimeAndPrivacy(t *testing.T) {
	good := nodeInfoYAML("SYNTHETIC_PRIVATE_NAME_SENTINEL | 剩余流量：123.45 GB")
	before := append([]byte(nil), good...)
	for _, checkedAt := range []time.Time{{}, time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if parseSubscriptionNodeInfo(good, checkedAt) != nil {
			t.Fatal("invalid check time was replaced by an invented current check")
		}
	}
	for _, content := range [][]byte{nil, []byte(""), []byte(strings.Repeat("#", maxSubscriptionNodeContent+1)), nodeInfoYAML(strings.Repeat("x", maxSubscriptionNodeName+1) + " 剩余流量：1 GB")} {
		if parseSubscriptionNodeInfo(content, nodeInfoCheckedAt) != nil {
			t.Fatal("empty or oversized node metadata was accepted")
		}
	}
	localTime := nodeInfoCheckedAt.In(time.FixedZone("synthetic-zone", 8*3600))
	got := parseSubscriptionNodeInfo(good, localTime)
	if got == nil || !reflect.DeepEqual(got.CheckedAt, localTime) || !bytes.Equal(before, good) {
		t.Fatal("existing read time or YAML was changed")
	}
	raw, err := json.Marshal(got)
	if err != nil || bytes.Contains(raw, []byte("SYNTHETIC_PRIVATE_NAME_SENTINEL")) {
		t.Fatal("metadata contains the synthetic private node name")
	}
	if !reflect.DeepEqual(got, parseSubscriptionNodeInfo(good, localTime)) {
		t.Fatal("pure read fallback is not deterministic")
	}
}

func TestSubscriptionInfoHeaderPriorityAndBlockedFields(t *testing.T) {
	content := nodeInfoYAML("Remaining:90 B", "Used:10 B", "Total:100 B", "Expires:2028-02-29")
	for _, tc := range []struct {
		name, header string
		want         *subscriptionTraffic
	}{
		{"no header", "", &subscriptionTraffic{Remaining: nodeInfoInt(90), Used: nodeInfoInt(10), Total: nodeInfoInt(100), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"unknown header", "plan=opaque", &subscriptionTraffic{Remaining: nodeInfoInt(90), Used: nodeInfoInt(10), Total: nodeInfoInt(100), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"all authoritative fields", "upload=1;download=2;total=5;expire=1900000000", &subscriptionTraffic{Upload: nodeInfoInt(1), Download: nodeInfoInt(2), Total: nodeInfoInt(5), Expire: nodeInfoInt(1900000000), Source: "header"}},
		{"header total and matching estimates", "total=100", &subscriptionTraffic{Total: nodeInfoInt(100), Used: nodeInfoInt(10), Remaining: nodeInfoInt(90), ExpireDate: "2028-02-29", Source: "mixed"}},
		{"header total contradicts node usage pair", "total=1000", &subscriptionTraffic{Total: nodeInfoInt(1000), ExpireDate: "2028-02-29", Source: "mixed"}},
		{"header expiry suppresses date hint", "expire=1900000000", &subscriptionTraffic{Total: nodeInfoInt(100), Used: nodeInfoInt(10), Remaining: nodeInfoInt(90), Expire: nodeInfoInt(1900000000), Source: "mixed"}},
		{"header legs used instead of estimated aggregate", "upload=4;download=6", &subscriptionTraffic{Upload: nodeInfoInt(4), Download: nodeInfoInt(6), Total: nodeInfoInt(100), ExpireDate: "2028-02-29", Source: "mixed"}},
		{"partial valid header can supplement", "upload=4", &subscriptionTraffic{Upload: nodeInfoInt(4), Total: nodeInfoInt(100), Used: nodeInfoInt(10), Remaining: nodeInfoInt(90), ExpireDate: "2028-02-29", Source: "mixed"}},
		{"node aggregates cannot contradict valid leg", "upload=20", &subscriptionTraffic{Upload: nodeInfoInt(20), Total: nodeInfoInt(100), ExpireDate: "2028-02-29", Source: "mixed"}},
		{"negative total cannot be repaired", "total=-1", &subscriptionTraffic{Used: nodeInfoInt(10), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"duplicate total cannot be repaired", "total=1;TOTAL=2", &subscriptionTraffic{Used: nodeInfoInt(10), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"identical duplicate total cannot be repaired", "total=100;total=100", &subscriptionTraffic{Used: nodeInfoInt(10), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"malformed total cannot be repaired", "total;total=100", &subscriptionTraffic{Used: nodeInfoInt(10), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"malformed total colon cannot be repaired", "total:100", &subscriptionTraffic{Used: nodeInfoInt(10), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"malformed total whitespace cannot be repaired", "total 100", &subscriptionTraffic{Used: nodeInfoInt(10), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"malformed expire colon cannot be repaired", "expire:1900000000", &subscriptionTraffic{Total: nodeInfoInt(100), Used: nodeInfoInt(10), Remaining: nodeInfoInt(90), Source: "nodes"}},
		{"unknown extension key does not block fallback", "total_bytes=999", &subscriptionTraffic{Total: nodeInfoInt(100), Used: nodeInfoInt(10), Remaining: nodeInfoInt(90), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"overflow total cannot be repaired", "total=9007199254740992", &subscriptionTraffic{Used: nodeInfoInt(10), ExpireDate: "2028-02-29", Source: "nodes"}},
		{"negative upload blocks indirect repair", "upload=-1;total=100", &subscriptionTraffic{Total: nodeInfoInt(100), ExpireDate: "2028-02-29", Source: "mixed"}},
		{"float download blocks indirect repair", "upload=1;download=1.5;total=100", &subscriptionTraffic{Upload: nodeInfoInt(1), Total: nodeInfoInt(100), ExpireDate: "2028-02-29", Source: "mixed"}},
		{"identical upload duplicate blocks indirect repair", "upload=1;UPLOAD=1;total=100", &subscriptionTraffic{Total: nodeInfoInt(100), ExpireDate: "2028-02-29", Source: "mixed"}},
		{"invalid expiry cannot be repaired", "expire=-1", &subscriptionTraffic{Total: nodeInfoInt(100), Used: nodeInfoInt(10), Remaining: nodeInfoInt(90), Source: "nodes"}},
		{"duplicate expiry cannot be repaired", "expire=1900000000;EXPIRE=1900000000", &subscriptionTraffic{Total: nodeInfoInt(100), Used: nodeInfoInt(10), Remaining: nodeInfoInt(90), Source: "nodes"}},
		{"overflow expiry cannot be repaired", "expire=253402300800", &subscriptionTraffic{Total: nodeInfoInt(100), Used: nodeInfoInt(10), Remaining: nodeInfoInt(90), Source: "nodes"}},
		{"overflowing header sum cannot be repaired", "upload=9007199254740991;download=1", &subscriptionTraffic{Upload: nodeInfoInt(maxSubscriptionTrafficValue), Download: nodeInfoInt(1), ExpireDate: "2028-02-29", Source: "mixed"}},
		{"oversized header fails closed", strings.Repeat("x", 8193), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now().UTC()
			got := parseSubscriptionInfo(tc.header, content)
			after := time.Now().UTC()
			if got != nil {
				if got.CheckedAt.Before(before) || got.CheckedAt.After(after) || got.CheckedAt.Location() != time.UTC {
					t.Fatal("HTTP information time not retained consistently")
				}
				got.CheckedAt = nodeInfoCheckedAt
			}
			assertNodeTraffic(t, got, tc.want)
		})
	}
	assertNodeTraffic(t, parseSubscriptionInfo("", []byte("invalid YAML [")), nil)
	got := parseSubscriptionInfo("total=100", []byte("invalid YAML ["))
	if got != nil {
		got.CheckedAt = nodeInfoCheckedAt
	}
	assertNodeTraffic(t, got, &subscriptionTraffic{Total: nodeInfoInt(100), Source: "header"})
}

func TestSubscriptionInfoMergeDoesNotModifyInputs(t *testing.T) {
	header, present := parseSubscriptionHeader("upload=4;download=6;total=100", nodeInfoCheckedAt)
	nodes := parseSubscriptionNodeInfo(nodeInfoYAML("Used:10 B", "Remaining:90 B", "Total:100 B"), nodeInfoCheckedAt)
	headerBefore, nodesBefore := *header, *nodes
	_ = mergeSubscriptionInfo(header, nodes, present, nodeInfoCheckedAt)
	if !reflect.DeepEqual(*header, headerBefore) || !reflect.DeepEqual(*nodes, nodesBefore) {
		t.Fatal("header/node merge changed inputs")
	}
}

func TestSubscriptionTrafficNewOptionalFieldsValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		traffic *subscriptionTraffic
		valid   bool
	}{
		{"nil", nil, true},
		{"legacy empty source", &subscriptionTraffic{Upload: nodeInfoInt(1), CheckedAt: nodeInfoCheckedAt}, true},
		{"remaining zero", &subscriptionTraffic{Remaining: nodeInfoInt(0), Source: "nodes", CheckedAt: nodeInfoCheckedAt}, true},
		{"used zero", &subscriptionTraffic{Used: nodeInfoInt(0), Source: "mixed", CheckedAt: nodeInfoCheckedAt}, true},
		{"remaining maximum", &subscriptionTraffic{Remaining: nodeInfoInt(maxSubscriptionTrafficValue), Source: "nodes", CheckedAt: nodeInfoCheckedAt}, true},
		{"used maximum", &subscriptionTraffic{Used: nodeInfoInt(maxSubscriptionTrafficValue), CheckedAt: nodeInfoCheckedAt}, true},
		{"date only", &subscriptionTraffic{ExpireDate: "2028-02-29", Source: "nodes", CheckedAt: nodeInfoCheckedAt}, true},
		{"negative remaining", &subscriptionTraffic{Remaining: nodeInfoInt(-1), CheckedAt: nodeInfoCheckedAt}, false},
		{"negative used", &subscriptionTraffic{Used: nodeInfoInt(-1), CheckedAt: nodeInfoCheckedAt}, false},
		{"oversized remaining", &subscriptionTraffic{Remaining: nodeInfoInt(maxSubscriptionTrafficValue + 1), CheckedAt: nodeInfoCheckedAt}, false},
		{"oversized used", &subscriptionTraffic{Used: nodeInfoInt(maxSubscriptionTrafficValue + 1), CheckedAt: nodeInfoCheckedAt}, false},
		{"source alone not metadata", &subscriptionTraffic{Source: "nodes", CheckedAt: nodeInfoCheckedAt}, false},
		{"unknown source", &subscriptionTraffic{Remaining: nodeInfoInt(1), Source: "node", CheckedAt: nodeInfoCheckedAt}, false},
		{"source case not canonical", &subscriptionTraffic{Remaining: nodeInfoInt(1), Source: "Header", CheckedAt: nodeInfoCheckedAt}, false},
		{"source cannot contain name", &subscriptionTraffic{Remaining: nodeInfoInt(1), Source: "SYNTHETIC_PRIVATE_NAME_SENTINEL", CheckedAt: nodeInfoCheckedAt}, false},
		{"zero read time", &subscriptionTraffic{Remaining: nodeInfoInt(1)}, false},
		{"invalid date", &subscriptionTraffic{ExpireDate: "2027-02-29", CheckedAt: nodeInfoCheckedAt}, false},
		{"date not canonical", &subscriptionTraffic{ExpireDate: "2028-2-29", CheckedAt: nodeInfoCheckedAt}, false},
		{"date wrong separator", &subscriptionTraffic{ExpireDate: "2028/02/29", CheckedAt: nodeInfoCheckedAt}, false},
		{"date with time", &subscriptionTraffic{ExpireDate: "2028-02-29T00:00:00Z", CheckedAt: nodeInfoCheckedAt}, false},
		{"zero date year", &subscriptionTraffic{ExpireDate: "0000-01-01", CheckedAt: nodeInfoCheckedAt}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if validSubscriptionTraffic(tc.traffic) != tc.valid {
				t.Fatal("optional field validation differs")
			}
		})
	}
}

func TestSubscriptionTrafficLegacyJSONCanonicalChecksum(t *testing.T) {
	for _, raw := range []string{
		`{"upload":1,"download":2,"total":10,"expire":1900000000,"checkedAt":"2026-11-22T05:00:00Z"}`,
		`{"total":10,"checkedAt":"2026-11-22T05:00:00Z"}`,
		`{"upload":0,"expire":0,"checkedAt":"2026-11-22T05:00:00Z"}`,
	} {
		var got subscriptionTraffic
		if err := json.Unmarshal([]byte(raw), &got); err != nil || !validSubscriptionTraffic(&got) {
			t.Fatal("old v1 metadata cannot be read")
		}
		encoded, err := json.Marshal(got)
		if err != nil || string(encoded) != raw || sha256.Sum256(encoded) != sha256.Sum256([]byte(raw)) {
			t.Fatal("new omitted fields changed legacy canonical bytes/checksum")
		}
		if got.Source != "" || got.Remaining != nil || got.Used != nil || got.ExpireDate != "" {
			t.Fatal("legacy optional fields were invented")
		}
	}
}
