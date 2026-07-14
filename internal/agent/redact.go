package agent

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/claracore/cora/internal/cora"
	"github.com/claracore/cora/internal/sanitize"
)

var (
	sensitiveKeyPattern = regexp.MustCompile(`(?i)(["']?(?:authorization|access[_-]?token|refresh[_-]?token|token|password|passwd|pwd|cardno)["']?\s*[:=]\s*["']?)(?:bearer\s+)?[^\s,"';}&]+`)
	sensitiveLabelName  = regexp.MustCompile(`(?i)^(?:authorization|access[_-]?token|refresh[_-]?token|token|password|passwd|pwd|cardno)$`)
	phonePattern        = regexp.MustCompile(`1[3-9][0-9]{9}`)
	identityPattern     = regexp.MustCompile(`[0-9]{17}[0-9Xx]`)
)

func redactEvent(event cora.Event) cora.Event {
	event.Message = redactText(event.Message)
	event.Stacktrace = redactText(event.Stacktrace)
	if event.Labels != nil {
		labels := make(map[string]string, len(event.Labels))
		for key, value := range event.Labels {
			if sensitiveLabelName.MatchString(key) {
				labels[key] = "[REDACTED]"
			} else {
				labels[key] = redactText(value)
			}
		}
		event.Labels = labels
	}
	breadcrumbs := append([]cora.Breadcrumb(nil), event.Breadcrumbs...)
	for index := range breadcrumbs {
		breadcrumbs[index].Message = redactText(breadcrumbs[index].Message)
	}
	event.Breadcrumbs = boundBreadcrumbs(breadcrumbs, defaultBreadcrumbMaxBytes)
	return event
}

func redactText(value string) string {
	value = sanitize.RedactSignedURLCredentials(value)
	value = sensitiveKeyPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = redactMatches(value, identityPattern, "[REDACTED_ID]", func(start, end int) bool {
		return (start == 0 || !asciiDigit(value[start-1])) &&
			(end == len(value) || !asciiAlphaNumeric(value[end]))
	})
	return redactMatches(value, phonePattern, "[REDACTED_PHONE]", func(start, end int) bool {
		return (start == 0 || !asciiDigit(value[start-1])) &&
			(end == len(value) || !asciiDigit(value[end]))
	})
}

func redactMatches(value string, pattern *regexp.Regexp, marker string, valid func(int, int) bool) string {
	matches := pattern.FindAllStringIndex(value, -1)
	var result strings.Builder
	last := 0
	for _, match := range matches {
		if !valid(match[0], match[1]) {
			continue
		}
		result.WriteString(value[last:match[0]])
		result.WriteString(marker)
		last = match[1]
	}
	if last == 0 {
		return value
	}
	result.WriteString(value[last:])
	return result.String()
}

func asciiDigit(value byte) bool { return value >= '0' && value <= '9' }

func asciiAlphaNumeric(value byte) bool {
	return asciiDigit(value) || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func boundBreadcrumbs(items []cora.Breadcrumb, maximum int) []cora.Breadcrumb {
	for len(items) > 0 {
		encoded, _ := json.Marshal(items)
		if len(encoded) <= maximum {
			return items
		}
		if len(items) > 1 {
			items = items[1:]
			continue
		}
		item := items[0]
		item.Message = ""
		overhead, _ := json.Marshal([]cora.Breadcrumb{item})
		if len(overhead) >= maximum {
			return nil
		}
		items[0].Message = truncateUTF8(items[0].Message, maximum-len(overhead))
		for {
			encoded, _ := json.Marshal(items)
			if len(encoded) <= maximum || items[0].Message == "" {
				break
			}
			next := len(items[0].Message) - (len(encoded) - maximum)
			if next >= len(items[0].Message) {
				next = len(items[0].Message) - 1
			}
			items[0].Message = truncateUTF8(items[0].Message, next)
		}
	}
	return items
}

func truncateUTF8(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return value[:maximum]
}
