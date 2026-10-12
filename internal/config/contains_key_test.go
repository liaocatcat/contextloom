package config

import "regexp"

// containsYAMLKey 精确匹配顶层 YAML 键（行首无缩进的 `key:`）。
func containsYAMLKey(s, key string) bool {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*$`)
	return re.MatchString(s)
}
