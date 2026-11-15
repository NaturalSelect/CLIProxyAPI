package config

import (
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestV8MigrationKeepsForkSections(t *testing.T) {
	var warnedMu sync.Mutex
	var warned []string
	SetV8MigrationWarnFunc(func(section, _ string) {
		warnedMu.Lock()
		warned = append(warned, section)
		warnedMu.Unlock()
	})
	t.Cleanup(func() {
		SetV8MigrationWarnFunc(nil)
	})

	legacy := []byte(`port: 8317
api-keys: ["client-key"]
api-key-model-rules:
  - api-key: client-key
    excluded-models: ["gpt-*"]
claude-prompt-cache:
  mode: adaptive
  cold-start-max-wait-seconds: 12
  diagnostics: true
codex:
  identity-confuse: true
`)
	migrated, _, errMigrate := NormalizeConfigLayout(legacy, true)
	if errMigrate != nil {
		t.Fatalf("NormalizeConfigLayout() error = %v", errMigrate)
	}
	warnedMu.Lock()
	if len(warned) != 0 {
		t.Fatalf("migration commented out fork sections %v:\n%s", warned, migrated)
	}
	warnedMu.Unlock()
	if errValidate := ValidateV8Config(migrated); errValidate != nil {
		t.Fatalf("ValidateV8Config() error = %v\n%s", errValidate, migrated)
	}

	var doc yaml.Node
	if errUnmarshal := yaml.Unmarshal(migrated, &doc); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	for _, path := range []string{"upstream.claude.prompt-cache.mode", "access.api-key-model-rules", "oauth.providers.codex.identity-confuse"} {
		if yamlPath(doc.Content[0], path) == nil {
			t.Errorf("migrated config is missing %s:\n%s", path, migrated)
		}
	}

	cfg, errParse := ParseConfigBytes(migrated)
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.ClaudePromptCache.EffectiveMode() != ClaudePromptCacheModeAdaptive {
		t.Fatalf("prompt-cache mode = %q, want %q", cfg.ClaudePromptCache.EffectiveMode(), ClaudePromptCacheModeAdaptive)
	}
	if cfg.ClaudePromptCache.EffectiveColdStartMaxWaitSeconds() != 12 {
		t.Fatalf("prompt-cache cold-start wait = %d, want 12", cfg.ClaudePromptCache.EffectiveColdStartMaxWaitSeconds())
	}
	if !cfg.ClaudePromptCache.Diagnostics {
		t.Fatal("prompt-cache diagnostics = false, want true")
	}
	if !cfg.Codex.IdentityConfuse {
		t.Fatal("codex identity-confuse = false, want true")
	}
	if len(cfg.APIKeyModelRules) != 1 || cfg.APIKeyModelRules[0].APIKey != "client-key" ||
		len(cfg.APIKeyModelRules[0].ExcludedModels) != 1 || cfg.APIKeyModelRules[0].ExcludedModels[0] != "gpt-*" {
		t.Fatalf("api-key-model-rules = %+v, want one rule excluding gpt-* for client-key", cfg.APIKeyModelRules)
	}
}
