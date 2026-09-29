package resources

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Trust remembers which project folders may load their own .pig files.
// Project files can run code (extensions) so we ask first.

func trustPath() string { return filepath.Join(GlobalDir(), "trust.json") }

// TrustDecision returns "yes", "no", or "" when nothing is saved for cwd.
func TrustDecision(cwd string) string {
	data, err := os.ReadFile(trustPath())
	if err != nil {
		return ""
	}
	var m map[string]bool
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	if v, ok := m[cwd]; ok {
		if v {
			return "yes"
		}
		return "no"
	}
	return ""
}

// SaveTrust records the decision for cwd.
func SaveTrust(cwd string, trusted bool) error {
	m := map[string]bool{}
	if data, err := os.ReadFile(trustPath()); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	m[cwd] = trusted
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(GlobalDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(trustPath(), data, 0o644)
}

// HasProjectFiles reports whether cwd has anything a trust decision guards.
func HasProjectFiles(cwd string) bool {
	pd := ProjectDir(cwd)
	for _, p := range []string{
		filepath.Join(pd, "settings.json"), filepath.Join(pd, "skills"), filepath.Join(pd, "prompts"),
		filepath.Join(pd, "extensions"), filepath.Join(pd, "SYSTEM.md"), filepath.Join(pd, "APPEND_SYSTEM.md"),
		filepath.Join(cwd, ".agents", "skills"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
