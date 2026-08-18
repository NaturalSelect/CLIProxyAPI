package config

import "testing"

func TestParseConfigBytesSanitizesAPIKeyModelRules(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`api-keys:
  - "sk-client-a"
api-key-model-rules:
  - api-key: " sk-client-a "
    excluded-models:
      - " GPT-5 "
      - "gpt-5"
      - "Claude-*"
  - api-key: "sk-client-a"
    excluded-models:
      - "gpt-4"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "sk-client-a" {
		t.Fatalf("APIKeys = %#v, want preserved legacy client key", cfg.APIKeys)
	}
	if len(cfg.APIKeyModelRules) != 1 {
		t.Fatalf("APIKeyModelRules length = %d, want 1", len(cfg.APIKeyModelRules))
	}

	rule := cfg.APIKeyModelRules[0]
	if rule.APIKey != "sk-client-a" {
		t.Fatalf("rule APIKey = %q, want %q", rule.APIKey, "sk-client-a")
	}

	wantModels := []string{"gpt-5", "claude-*", "gpt-4"}
	if len(rule.ExcludedModels) != len(wantModels) {
		t.Fatalf("ExcludedModels = %#v, want %#v", rule.ExcludedModels, wantModels)
	}
	for index, wantModel := range wantModels {
		if rule.ExcludedModels[index] != wantModel {
			t.Fatalf("ExcludedModels[%d] = %q, want %q", index, rule.ExcludedModels[index], wantModel)
		}
	}
}

func TestParseConfigBytesDropsEmptyAPIKeyModelRules(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`api-keys:
  - "sk-client-a"
api-key-model-rules:
  - api-key: ""
    excluded-models:
      - "gpt-5"
  - api-key: "sk-client-b"
    excluded-models: []
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.APIKeyModelRules) != 0 {
		t.Fatalf("APIKeyModelRules = %#v, want empty after dropping invalid entries", cfg.APIKeyModelRules)
	}
}

func TestParseConfigBytesWithoutAPIKeyModelRulesLeavesLegacyConfigUnchanged(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`api-keys:
  - "sk-old-a"
  - "sk-old-b"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("APIKeys = %#v, want 2 legacy entries", cfg.APIKeys)
	}
	if len(cfg.APIKeyModelRules) != 0 {
		t.Fatalf("APIKeyModelRules = %#v, want nil when absent from config", cfg.APIKeyModelRules)
	}
}
