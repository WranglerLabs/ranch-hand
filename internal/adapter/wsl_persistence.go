package adapter

import (
	"bytes"
	"strings"
)

var requiredWSLPersistence = []struct {
	section string
	key     string
}{
	{section: "general", key: "instanceIdleTimeout"},
	{section: "wsl2", key: "vmIdleTimeout"},
}

func wslConfigHasPersistence(contents []byte) bool {
	values := wslConfigValues(contents)
	for _, required := range requiredWSLPersistence {
		if values[strings.ToLower(required.section)+"\x00"+strings.ToLower(required.key)] != "-1" {
			return false
		}
	}
	return true
}

func patchWSLConfig(contents []byte) ([]byte, bool) {
	if wslConfigHasPersistence(contents) {
		return contents, false
	}
	newline := "\n"
	if bytes.Contains(contents, []byte("\r\n")) {
		newline = "\r\n"
	}
	normalized := strings.ReplaceAll(string(contents), "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	lines := []string{}
	if normalized != "" {
		lines = strings.Split(normalized, "\n")
	}
	for _, required := range requiredWSLPersistence {
		lines = setWSLConfigValue(lines, required.section, required.key, "-1")
	}
	return []byte(strings.Join(lines, newline) + newline), true
}

func wslConfigValues(contents []byte) map[string]string {
	result := map[string]string{}
	section := ""
	for _, raw := range strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1:strings.Index(line, "]")]))
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			result[section+"\x00"+strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

func setWSLConfigValue(lines []string, section, key, value string) []string {
	sectionStart := -1
	for index, raw := range lines {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if !strings.HasPrefix(line, "[") || !strings.Contains(line, "]") {
			continue
		}
		currentSection := strings.TrimSpace(line[1:strings.Index(line, "]")])
		if strings.EqualFold(currentSection, section) {
			sectionStart = index
		}
	}
	if sectionStart < 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		return append(lines, "["+section+"]", key+"="+value)
	}
	sectionEnd := len(lines)
	for index := sectionStart + 1; index < len(lines); index++ {
		line := strings.TrimSpace(strings.TrimPrefix(lines[index], "\ufeff"))
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			sectionEnd = index
			break
		}
	}
	updated := false
	for index := sectionStart + 1; index < sectionEnd; index++ {
		line := strings.TrimSpace(lines[index])
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), key) {
			lines[index] = key + "=" + value
			updated = true
		}
	}
	if updated {
		return lines
	}
	lines = append(lines, "")
	copy(lines[sectionEnd+1:], lines[sectionEnd:])
	lines[sectionEnd] = key + "=" + value
	return lines
}
