package main

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Use JSON integers that the Web UI can represent exactly. Invalid optional
// fields remain unknown rather than rounding a quota or inventing unlimited use.
const maxSubscriptionTrafficValue int64 = (1 << 53) - 1
const maxSubscriptionExpiry int64 = 253402300799 // 9999-12-31T23:59:59Z

type subscriptionTraffic struct {
	Upload    *int64    `json:"upload,omitempty"`
	Download  *int64    `json:"download,omitempty"`
	Total     *int64    `json:"total,omitempty"`
	Expire    *int64    `json:"expire,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	// Append optional fields: old v1 backup JSON keeps its canonical field order.
	Remaining  *int64 `json:"remaining,omitempty"`
	Used       *int64 `json:"used,omitempty"`
	Source     string `json:"source,omitempty"`
	ExpireDate string `json:"expireDate,omitempty"`
}

type subscriptionDownload struct {
	Content []byte
	Traffic *subscriptionTraffic
}

func parseSubscriptionUserinfo(raw string) *subscriptionTraffic {
	traffic, _ := parseSubscriptionHeader(raw, time.Now().UTC())
	return traffic
}

// Keep presence separately from validity: an invalid or duplicate header field
// is not a missing field that a lower-confidence node hint may repair.
func parseSubscriptionHeader(raw string, checkedAt time.Time) (*subscriptionTraffic, map[string]bool) {
	if strings.TrimSpace(raw) == "" || len(raw) > 8192 {
		return nil, nil
	}
	traffic := &subscriptionTraffic{CheckedAt: checkedAt, Source: "header"}
	fields := map[string]**int64{"upload": &traffic.Upload, "download": &traffic.Download, "total": &traffic.Total, "expire": &traffic.Expire}
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		key = strings.ToLower(strings.TrimSpace(key))
		field, known := fields[key]
		if !known {
			// A malformed known declaration such as "total:10" or "total 10"
			// must not look absent to the node fallback. Unknown extension keys
			// such as total_bytes remain genuinely unknown, not standard fields.
			declaration := strings.ToLower(strings.TrimSpace(part))
			for candidate, pointer := range fields {
				if strings.HasPrefix(declaration, candidate) && len(declaration) > len(candidate) {
					next, _ := utf8.DecodeRuneInString(declaration[len(candidate):])
					if unicode.IsSpace(next) || next == ':' || next == '：' {
						key, field, known, ok = candidate, pointer, true, false
						break
					}
				}
			}
			if !known {
				continue
			}
		}
		// Even an identical or invalid duplicate is ambiguous. Do not pick an
		// arbitrary winner from multiple header lines or differently cased keys.
		if seen[key] {
			*field = nil
			continue
		}
		seen[key] = true
		value = strings.TrimSpace(value)
		if !ok || value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			continue
		}
		number, err := strconv.ParseInt(value, 10, 64)
		limit := maxSubscriptionTrafficValue
		if key == "expire" {
			limit = maxSubscriptionExpiry
		}
		if err != nil || number < 0 || number > limit {
			continue
		}
		parsed := new(int64)
		*parsed = number
		*field = parsed
	}
	if traffic.Upload == nil && traffic.Download == nil && traffic.Total == nil && traffic.Expire == nil {
		return nil, seen
	}
	return traffic, seen
}

// parseSubscriptionInfo is for a successful HTTP response. Header bytes are
// authoritative; node names only supplement genuinely absent information.
func parseSubscriptionInfo(raw string, content []byte) *subscriptionTraffic {
	if len(raw) > 8192 {
		// We cannot establish which fields an oversized header tried to declare.
		return nil
	}
	checkedAt := time.Now().UTC()
	header, present := parseSubscriptionHeader(raw, checkedAt)
	nodes := parseSubscriptionNodeInfo(content, checkedAt)
	return mergeSubscriptionInfo(header, nodes, present, checkedAt)
}

// This internal merge requires raw-header presence information. In particular,
// do not use it to fill nil fields in old persisted header metadata: nil there
// cannot distinguish absent fields from fields rejected as invalid.
func mergeSubscriptionInfo(header, nodes *subscriptionTraffic, present map[string]bool, checkedAt time.Time) *subscriptionTraffic {
	if nodes == nil {
		return header
	}
	result := *nodes
	result.CheckedAt = checkedAt
	if header == nil {
		header = &subscriptionTraffic{}
	}
	result.Upload, result.Download, result.Expire = header.Upload, header.Download, header.Expire
	if present["total"] {
		result.Total = header.Total
	}
	if present["expire"] {
		result.ExpireDate = ""
	}

	invalidUsage := (present["upload"] && header.Upload == nil) || (present["download"] && header.Download == nil)
	headerUsedKnown := header.Upload != nil && header.Download != nil
	var headerUsed int64
	if headerUsedKnown {
		if *header.Upload > maxSubscriptionTrafficValue-*header.Download {
			invalidUsage = true
		} else {
			headerUsed = *header.Upload + *header.Download
		}
	}
	// Used/remaining must not indirectly repair invalid upload/download or an
	// overflowing header aggregate, even though nodes do not provide either leg.
	if invalidUsage || headerUsedKnown {
		result.Used = nil
	}
	if invalidUsage || (present["total"] && header.Total == nil) {
		result.Remaining = nil
	}
	if result.Used != nil && ((header.Upload != nil && *result.Used < *header.Upload) || (header.Download != nil && *result.Used < *header.Download)) {
		result.Used = nil
	}
	if result.Total != nil {
		if !present["total"] && ((header.Upload != nil && *header.Upload > *result.Total) || (header.Download != nil && *header.Download > *result.Total) || (headerUsedKnown && !invalidUsage && headerUsed > *result.Total)) {
			// An estimated total cannot contradict authoritative usage.
			result.Total, result.Remaining = nil, nil
		} else {
			if headerUsedKnown && !invalidUsage {
				// The UI can calculate this from the preferred total and header legs.
				result.Remaining = nil
			}
			if result.Used != nil && *result.Used > *result.Total {
				result.Used = nil
			}
			if result.Remaining != nil && (*result.Remaining > *result.Total || (header.Upload != nil && *result.Remaining > max(int64(0), *result.Total-*header.Upload)) || (header.Download != nil && *result.Remaining > max(int64(0), *result.Total-*header.Download))) {
				result.Remaining = nil
			}
			if result.Used != nil && result.Remaining != nil && !subscriptionNodeQuotaAddsUp(*result.Used, *result.Remaining, *result.Total) {
				result.Used, result.Remaining = nil, nil
			}
		}
	}
	hasHeader := result.Upload != nil || result.Download != nil || result.Expire != nil || (present["total"] && result.Total != nil)
	hasNodes := result.Used != nil || result.Remaining != nil || result.ExpireDate != "" || (!present["total"] && result.Total != nil)
	switch {
	case hasHeader && hasNodes:
		result.Source = "mixed"
	case hasHeader:
		result.Source = "header"
	case hasNodes:
		result.Source = "nodes"
	default:
		return nil
	}
	return &result
}

const maxSubscriptionNodeContent = 4 << 20 // same bound as subscription download
const maxSubscriptionNodeName = 8192

// Only the direct proxies[].name string projection is read. No alias expansion,
// provider fetching, recursive text search, YAML rewriting, or names in results.
// The caller supplies the existing read/download time; this is not a new check.
func parseSubscriptionNodeInfo(content []byte, checkedAt time.Time) *subscriptionTraffic {
	if len(content) == 0 || len(content) > maxSubscriptionNodeContent || checkedAt.IsZero() || checkedAt.Year() < 1 || checkedAt.Year() > 9999 {
		return nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var doc, extra yaml.Node
	if decoder.Decode(&doc) != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil
	}
	if decoder.Decode(&extra) != io.EOF {
		return nil // multiple documents (even empty ones) are ambiguous
	}
	root, ok := subscriptionNodeMapping(doc.Content[0])
	if !ok {
		return nil
	}
	proxies := root["proxies"]
	if proxies == nil || proxies.Kind != yaml.SequenceNode || proxies.Tag != "!!seq" {
		return nil
	}
	var hints subscriptionNodeHints
	for _, proxy := range proxies.Content {
		fields, ok := subscriptionNodeMapping(proxy)
		if !ok {
			return nil // skipping an alias/merge could hide a conflicting hint
		}
		name := fields["name"]
		if name == nil {
			continue
		}
		if name.Kind != yaml.ScalarNode || name.Tag != "!!str" || len(name.Value) > maxSubscriptionNodeName {
			return nil
		}
		hints.addName(name.Value)
	}
	remaining, used, total := hints.bytes[0].get(), hints.bytes[1].get(), hints.bytes[2].get()
	// Preserve independent optional values, but do not select a winner when the
	// supplied quota fields themselves contradict each other.
	badTotal, badUsed, badRemaining := false, false, false
	if total != nil {
		if remaining != nil && *remaining > *total {
			badTotal, badRemaining = true, true
		}
		if used != nil && *used > *total {
			badTotal, badUsed = true, true
		}
		if used != nil && remaining != nil && !subscriptionNodeQuotaAddsUp(*used, *remaining, *total) {
			badTotal, badUsed, badRemaining = true, true, true
		}
	}
	if badTotal {
		total = nil
	}
	if badUsed {
		used = nil
	}
	if badRemaining {
		remaining = nil
	}
	traffic := &subscriptionTraffic{Remaining: remaining, Used: used, Total: total, ExpireDate: hints.date.get(), Source: "nodes", CheckedAt: checkedAt}
	if !validSubscriptionTraffic(traffic) {
		return nil
	}
	return traffic
}

func subscriptionNodeMapping(node *yaml.Node) (map[string]*yaml.Node, bool) {
	if node == nil || node.Kind != yaml.MappingNode || node.Tag != "!!map" || len(node.Content)%2 != 0 {
		return nil, false
	}
	fields := make(map[string]*yaml.Node, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" {
			return nil, false
		}
		if _, duplicate := fields[key.Value]; duplicate {
			return nil, false
		}
		fields[key.Value] = node.Content[i+1]
	}
	return fields, true
}

// Each value is bounded by 2^53-1, so this sum cannot overflow int64. Flooring
// fractional bytes independently may make used+remaining exactly one byte lower
// than total; larger discrepancies are conflicting, not a basis for inference.
func subscriptionNodeQuotaAddsUp(used, remaining, total int64) bool {
	difference := total - (used + remaining)
	return difference == 0 || difference == 1
}

type subscriptionNodeByteHint struct {
	value *int64
	bad   bool
}

func (hint *subscriptionNodeByteHint) add(value *int64) {
	if value == nil || (hint.value != nil && *hint.value != *value) {
		hint.bad = true
		return
	}
	hint.value = value
}

func (hint *subscriptionNodeByteHint) get() *int64 {
	if hint.bad {
		return nil
	}
	return hint.value
}

type subscriptionNodeDateHint struct {
	value string
	bad   bool
}

func (hint *subscriptionNodeDateHint) add(value string) {
	if value == "" || (hint.value != "" && hint.value != value) {
		hint.bad = true
		return
	}
	hint.value = value
}

func (hint *subscriptionNodeDateHint) get() string {
	if hint.bad {
		return ""
	}
	return hint.value
}

type subscriptionNodeHints struct {
	bytes [3]subscriptionNodeByteHint // remaining, used, total (never inferred)
	date  subscriptionNodeDateHint
}

var subscriptionNodeLabels = regexp.MustCompile(`(?i)(剩余[\s\p{Zs}]*(?:流量|额度)?|(?:流量|额度)[\s\p{Zs}]*剩余|可用[\s\p{Zs}]*(?:流量|额度)|(?:remaining|available)(?:[\s\p{Zs}]+(?:traffic|data|quota|bandwidth))?|(?:traffic|data|quota|bandwidth)[\s\p{Zs}]+(?:remaining|left))|(已(?:使用|用|消耗)[\s\p{Zs}]*(?:流量|额度)?|(?:流量|额度)[\s\p{Zs}]*已(?:使用|用)|(?:used|consumed|usage)(?:[\s\p{Zs}]+(?:traffic|data|quota|bandwidth))?|(?:traffic|data|quota|bandwidth)[\s\p{Zs}]+(?:used|consumed|usage))|(总[\s\p{Zs}]*(?:流量|额度|额|量)|总计(?:流量)?|(?:流量|额度)[\s\p{Zs}]*总(?:额|量)?|套餐[\s\p{Zs}]*(?:流量|额度)|total(?:[\s\p{Zs}]+(?:traffic|data|quota|bandwidth))?|(?:traffic|data|quota|bandwidth)[\s\p{Zs}]+total)|(到期[\s\p{Zs}]*(?:时间|日期|日)?|过期(?:时间|日期)?|有效期(?:至)?|expires(?:[\s\p{Zs}]+(?:on|at|date|time))?|(?:expiration|expiry|expire)(?:[\s\p{Zs}]+(?:date|time))?|valid[\s\p{Zs}]+until)`)
var subscriptionNodeQuantity = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)[\s\p{Zs}]*(TiB|GiB|MiB|KiB|TB|GB|MB|KB|B)`)
var subscriptionNodeByteUnit = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:[kmgtpezy]i?b|b)(?:$|[^a-z])`)
var subscriptionNodeExtraQuantity = regexp.MustCompile(`(?i)[0-9]+(?:\.[0-9]+)?[\s\p{Zs}]*(TiB|GiB|MiB|KiB|TB|GB|MB|KB|B)`)
var subscriptionNodeRateSuffix = regexp.MustCompile(`(?i)^[\s\p{Zs}]*(?:per[\s\p{Zs}]+(?:second|sec|s)\b|每秒)`)
var subscriptionNodeDate = regexp.MustCompile(`^([0-9]{4})([-/.])([0-9]{1,2})([-/.])([0-9]{1,2})`)
var subscriptionNodeChineseDate = regexp.MustCompile(`^([0-9]{4})年([0-9]{1,2})月([0-9]{1,2})日`)
var subscriptionNodeDateAttempt = regexp.MustCompile(`^[0-9]{4}[-/.年]`)
var subscriptionNodeAnotherDate = regexp.MustCompile(`[0-9]{4}[-/.年]`)

func (hints *subscriptionNodeHints) addName(name string) {
	type label struct{ kind, start, end int }
	var labels []label
	for _, indexes := range subscriptionNodeLabels.FindAllStringSubmatchIndex(name, -1) {
		start, end := indexes[0], indexes[1]
		if name[start] < utf8.RuneSelf {
			// Avoid ordinary names such as Unused, Totality or Remaining2x.
			word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
			if start > 0 {
				r, _ := utf8.DecodeLastRuneInString(name[:start])
				if word(r) {
					continue
				}
			}
			if end < len(name) {
				r, _ := utf8.DecodeRuneInString(name[end:])
				if word(r) {
					continue
				}
			}
		}
		for kind := 0; kind < 4; kind++ {
			if indexes[2+kind*2] >= 0 {
				labels = append(labels, label{kind, start, end})
				break
			}
		}
	}
	for i, label := range labels {
		end := len(name)
		if i+1 < len(labels) {
			end = labels[i+1].start
		}
		value := strings.TrimLeftFunc(name[label.end:end], subscriptionNodeValueDecoration)
		if label.kind < 3 {
			if parsed, attempted := parseSubscriptionNodeBytes(value); attempted {
				hints.bytes[label.kind].add(parsed)
			}
		} else if parsed, attempted := parseSubscriptionNodeDate(value); attempted {
			hints.date.add(parsed)
		}
	}
}

func subscriptionNodeValueDecoration(r rune) bool {
	if unicode.IsSpace(r) || strings.ContainsRune(":：=|[【(（\"'", r) {
		return true
	}
	// Icons may surround the label/value; never strip signs or multiplication.
	if r == '➕' || r == '➖' || r == '➗' || r == '✕' || r == '✖' {
		return false
	}
	return (r >= 0x1f000 && r <= 0x1faff) || (r >= 0x2600 && r <= 0x27bf) || r == 0xfe0e || r == 0xfe0f || r == 0x200d
}

func subscriptionNodeValueSuffixOK(suffix string) bool {
	if suffix == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(suffix)
	if (r < utf8.RuneSelf && unicode.IsLetter(r)) || unicode.IsDigit(r) || r == '_' || r == '.' {
		return false // GBps, GB2, a partial decimal, etc.
	}
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return true
	}
	r, size := utf8.DecodeRuneInString(suffix)
	if strings.ContainsRune("/\\-+−－＋‐‑‒–—―➖➕➗", r) {
		return false // a rate, ratio, range or signed continuation
	}
	if r == ',' || r == '，' {
		tail := strings.TrimSpace(suffix[size:])
		if tail != "" {
			next, _ := utf8.DecodeRuneInString(tail)
			if unicode.IsDigit(next) || strings.ContainsRune(".+-−", next) {
				return false
			}
		}
	}
	return true
}

func parseSubscriptionNodeBytes(value string) (*int64, bool) {
	match := subscriptionNodeQuantity.FindStringSubmatchIndex(value)
	if match == nil {
		// Bare 'Remaining 5 days' and node speed/multiplier labels are not quota
		// declarations. Malformed byte declarations still poison that field.
		return nil, subscriptionNodeByteUnit.MatchString(value)
	}
	suffix := value[match[1]:]
	if !subscriptionNodeValueSuffixOK(suffix) || subscriptionNodeExtraQuantity.MatchString(suffix) || subscriptionNodeRateSuffix.MatchString(suffix) {
		return nil, true
	}
	amount, unit := value[match[2]:match[3]], strings.ToUpper(value[match[4]:match[5]])
	multiplier := map[string]int64{"B": 1, "KB": 1 << 10, "KIB": 1 << 10, "MB": 1 << 20, "MIB": 1 << 20, "GB": 1 << 30, "GIB": 1 << 30, "TB": 1 << 40, "TIB": 1 << 40}[unit]
	whole, fractional, _ := strings.Cut(amount, ".")
	numerator, ok := new(big.Int).SetString(whole+fractional, 10)
	if !ok {
		return nil, true
	}
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fractional))), nil)
	numerator.Mul(numerator, big.NewInt(multiplier))
	limit := new(big.Int).Mul(big.NewInt(maxSubscriptionTrafficValue), denominator)
	if numerator.Cmp(limit) > 0 {
		return nil, true // compare before flooring: max+0.1 B is still out of range
	}
	// Airport KB/MB/GB/TB convention and explicit IEC both use powers of 1024.
	// Convert exactly without float64, then floor sub-byte fractions (never round
	// a quota up). UI must identify these as node hints/estimates, GB by 1024.
	number := new(big.Int).Quo(numerator, denominator).Int64()
	return &number, true
}

func parseSubscriptionNodeDate(value string) (string, bool) {
	match := subscriptionNodeDate.FindStringSubmatchIndex(value)
	var year, month, day, end int
	if match != nil {
		if value[match[4]:match[5]] != value[match[8]:match[9]] {
			return "", true
		}
		year, _ = strconv.Atoi(value[match[2]:match[3]])
		month, _ = strconv.Atoi(value[match[6]:match[7]])
		day, _ = strconv.Atoi(value[match[10]:match[11]])
		end = match[1]
	} else if match = subscriptionNodeChineseDate.FindStringSubmatchIndex(value); match != nil {
		year, _ = strconv.Atoi(value[match[2]:match[3]])
		month, _ = strconv.Atoi(value[match[4]:match[5]])
		day, _ = strconv.Atoi(value[match[6]:match[7]])
		end = match[1]
	} else {
		return "", subscriptionNodeDateAttempt.MatchString(value)
	}
	suffix := value[end:]
	trimmed := strings.TrimSpace(suffix)
	if !subscriptionNodeValueSuffixOK(suffix) || subscriptionNodeAnotherDate.MatchString(suffix) || strings.HasPrefix(trimmed, "至") || strings.HasPrefix(trimmed, "到") || strings.HasPrefix(strings.ToLower(trimmed), "to ") {
		return "", true
	}
	date := fmt.Sprintf("%04d-%02d-%02d", year, month, day)
	if !validSubscriptionExpireDate(date) {
		return "", true
	}
	return date, true // no time zone or exact instant is guessed from a date
}

func validSubscriptionExpireDate(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Year() >= 1 && parsed.Year() <= 9999 && parsed.Format("2006-01-02") == value
}

func validSubscriptionTraffic(traffic *subscriptionTraffic) bool {
	if traffic == nil {
		return true
	}
	if traffic.CheckedAt.IsZero() || traffic.CheckedAt.Year() < 1 || traffic.CheckedAt.Year() > 9999 {
		return false
	}
	if traffic.Source != "" && traffic.Source != "header" && traffic.Source != "nodes" && traffic.Source != "mixed" {
		return false
	}
	known := false
	for _, value := range []*int64{traffic.Upload, traffic.Download, traffic.Total, traffic.Remaining, traffic.Used} {
		if value != nil {
			known = true
			if *value < 0 || *value > maxSubscriptionTrafficValue {
				return false
			}
		}
	}
	if traffic.Expire != nil {
		known = true
		if *traffic.Expire < 0 || *traffic.Expire > maxSubscriptionExpiry {
			return false
		}
	}
	if traffic.ExpireDate != "" {
		known = true
		if !validSubscriptionExpireDate(traffic.ExpireDate) {
			return false
		}
	}
	return known
}
