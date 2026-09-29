package resources

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings mirror settings.json. Project settings override global ones.
type Settings struct {
	DefaultProvider      string `json:"defaultProvider,omitempty"`
	DefaultModel         string `json:"defaultModel,omitempty"`
	DefaultThinkingLevel string `json:"defaultThinkingLevel,omitempty"`
	DefaultProjectTrust  string `json:"defaultProjectTrust,omitempty"` // ask | always | never
	HideThinkingBlock    *bool  `json:"hideThinkingBlock,omitempty"`
	ShellPath            string `json:"shellPath,omitempty"`
	SessionDir           string `json:"sessionDir,omitempty"`
	SteeringMode         string `json:"steeringMode,omitempty"` // one-at-a-time | all
	FollowUpMode         string `json:"followUpMode,omitempty"`
	QuietStartup         *bool  `json:"quietStartup,omitempty"`
	Compaction           struct {
		Enabled          *bool `json:"enabled,omitempty"`
		ReserveTokens    int   `json:"reserveTokens,omitempty"`
		KeepRecentTokens int   `json:"keepRecentTokens,omitempty"`
	} `json:"compaction"`
	Retry struct {
		Enabled    *bool `json:"enabled,omitempty"`
		MaxRetries int   `json:"maxRetries,omitempty"`
	} `json:"retry"`
	Skills     []string `json:"skills,omitempty"`
	Prompts    []string `json:"prompts,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
}

// LoadSettings reads global then project settings. Missing files are fine.
// Project settings are only applied when trusted.
func LoadSettings(cwd string, trusted bool) Settings {
	var s Settings
	s.readInto(filepath.Join(GlobalDir(), "settings.json"))
	if trusted {
		s.readInto(filepath.Join(ProjectDir(cwd), "settings.json"))
	}
	if s.DefaultProjectTrust == "" {
		s.DefaultProjectTrust = "ask"
	}
	if s.Compaction.ReserveTokens == 0 {
		s.Compaction.ReserveTokens = 16384
	}
	if s.Compaction.KeepRecentTokens == 0 {
		s.Compaction.KeepRecentTokens = 20000
	}
	if s.Retry.MaxRetries == 0 {
		s.Retry.MaxRetries = 3
	}
	return s
}

// readInto merges one file over the current values; keys present win.
func (s *Settings) readInto(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, s)
}

// CompactionEnabled defaults to true.
func (s Settings) CompactionEnabled() bool {
	return s.Compaction.Enabled == nil || *s.Compaction.Enabled
}

// SaveGlobal writes a few keys back into the global settings file.
func SaveGlobal(update func(m map[string]any)) error {
	path := filepath.Join(GlobalDir(), "settings.json")
	m := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	update(m)
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
