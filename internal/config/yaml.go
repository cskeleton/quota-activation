package config

import (
	"fmt"
	"strconv"
	"strings"
)

func parseYAMLMap(data string) (map[string]any, error) {
	result := map[string]any{}
	activationModels := map[string]any{}
	codex := map[string]any{}
	antigravity := map[string]any{}
	section := yamlSection{}

	for line := range strings.SplitSeq(data, "\n") {
		item, ok, err := parseYAMLLine(line)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if applyDottedActivationModelsYAML(item, result, activationModels, codex, antigravity) {
			continue
		}
		if item.indent == 0 {
			section = yamlSection{name: item.key, indent: item.indent, subIndent: -1}
			if item.key == "activation_models" {
				result[item.key] = activationModels
				continue
			}
			result[item.key] = yamlScalarForKey(item.key, item.value)
			continue
		}
		applyActivationModelsYAML(item, &section, activationModels, codex, antigravity)
	}
	return result, nil
}

type yamlSection struct {
	name      string
	sub       string
	indent    int
	subIndent int
}

type yamlLine struct {
	key    string
	value  string
	indent int
}

func parseYAMLLine(line string) (yamlLine, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return yamlLine{}, false, nil
	}
	// 宿主商店安装会把 store.tags 等序列写入 config_yaml（如 `- quota`）。
	// 文档约定：enabled/priority 外字段原样传入；插件只需解析自有键，列表项应忽略而非 fail-closed。
	if strings.HasPrefix(trimmed, "-") {
		return yamlLine{}, false, nil
	}
	key, value, ok := strings.Cut(trimmed, ":")
	if !ok {
		return yamlLine{}, false, fmt.Errorf("must use key: value syntax in line: %s", trimmed)
	}
	return yamlLine{key: strings.TrimSpace(key), value: strings.TrimSpace(value), indent: len(line) - len(strings.TrimLeft(line, " "))}, true, nil
}

func applyActivationModelsYAML(item yamlLine, section *yamlSection, activationModels map[string]any, codex map[string]any, antigravity map[string]any) {
	if section.name != "activation_models" || item.indent <= section.indent {
		return
	}
	if item.key == "codex" && item.value == "" {
		section.sub = item.key
		section.subIndent = item.indent
		activationModels[item.key] = codex
		return
	}
	if item.key == "antigravity" && item.value == "" {
		section.sub = item.key
		section.subIndent = item.indent
		activationModels[item.key] = antigravity
		return
	}
	if section.sub == "codex" && item.indent > section.subIndent {
		codex[item.key] = yamlScalarForKey(item.key, item.value)
		return
	}
	if section.sub == "antigravity" && item.indent > section.subIndent {
		antigravity[item.key] = yamlScalarForKey(item.key, item.value)
		return
	}
	activationModels[item.key] = yamlScalarForKey(item.key, item.value)
}

func applyDottedActivationModelsYAML(item yamlLine, result map[string]any, activationModels map[string]any, codex map[string]any, antigravity map[string]any) bool {
	switch item.key {
	case "activation_models.codex":
		result["activation_models"] = activationModels
		activationModels["codex"] = yamlScalarForKey("codex", item.value)
		return true
	case "activation_models.codex.models":
		result["activation_models"] = activationModels
		activationModels["codex"] = codex
		codex["models"] = yamlScalarForKey("models", item.value)
		return true
	case "activation_models.antigravity.models_group":
		result["activation_models"] = activationModels
		activationModels["antigravity"] = antigravity
		antigravity["models_group"] = yamlScalarForKey("models_group", item.value)
		return true
	case "activation_models.antigravity.models":
		result["activation_models"] = activationModels
		activationModels["antigravity"] = antigravity
		antigravity["models"] = yamlScalarForKey("models", item.value)
		return true
	case "activation_models.antigravity.gemini":
		result["activation_models"] = activationModels
		activationModels["antigravity"] = antigravity
		antigravity["gemini"] = yamlScalarForKey("gemini", item.value)
		return true
	case "activation_models.antigravity.claude_gpt":
		result["activation_models"] = activationModels
		activationModels["antigravity"] = antigravity
		antigravity["claude_gpt"] = yamlScalarForKey("claude_gpt", item.value)
		return true
	case "activation_models.antigravity.enable_gemini":
		result["activation_models"] = activationModels
		activationModels["antigravity"] = antigravity
		antigravity["enable_gemini"] = yamlScalarForKey("enable_gemini", item.value)
		return true
	case "activation_models.antigravity.enable_claude_gpt":
		result["activation_models"] = activationModels
		activationModels["antigravity"] = antigravity
		antigravity["enable_claude_gpt"] = yamlScalarForKey("enable_claude_gpt", item.value)
		return true
	default:
		return false
	}
}

func yamlScalarForKey(key string, value string) any {
	switch key {
	case "models_group", "models":
		return yamlText(value)
	case "max_concurrency", "priority":
		return yamlScalar(value)
	case "auto_activate", "enable_before_activation", "enable_5h_window", "scheduler_boost_fallback", "enabled", "enable_gemini", "enable_claude_gpt":
		return yamlScalar(value)
	default:
		text := yamlText(value)
		if text == "" {
			return nil
		}
		return text
	}
}

func yamlScalar(value string) any {
	text := yamlText(value)
	if text == "" {
		return nil
	}
	if parsed, err := strconv.Atoi(text); err == nil {
		return parsed
	}
	if parsed, err := strconv.ParseBool(text); err == nil {
		return parsed
	}
	return text
}

func yamlText(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > 1 && trimmed[0] == trimmed[len(trimmed)-1] && (trimmed[0] == '"' || trimmed[0] == '\'') {
		return trimmed[1 : len(trimmed)-1]
	}
	return trimmed
}
