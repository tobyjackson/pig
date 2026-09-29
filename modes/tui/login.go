package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/resources"
)

// pendingLogin holds the provider name while we wait for a pasted key.
// It lives on the model; see submitLogin.

func (m *model) submitLogin(text string) (tea.Model, tea.Cmd) {
	provider := strings.ToLower(m.pendingLogin)
	m.pendingLogin = ""
	m.ta.Placeholder = "Ask anything. Enter sends, Ctrl+J adds a line, / for commands, ! for shell."
	key := strings.TrimSpace(text)
	if key == "" {
		m.note("login cancelled")
		return m, nil
	}
	known := false
	for _, p := range m.s.Registry.Providers() {
		if p == provider {
			known = true
		}
	}
	if !known {
		msg := "unknown provider " + provider
		if c := m.s.Registry.ClosestProvider(provider); c != "" {
			msg += "; did you mean /login " + c + "?"
		}
		m.note(msg)
		return m, nil
	}
	if err := ai.SaveAuth(resources.GlobalDir(), provider, key); err != nil {
		m.note("could not save key: " + err.Error())
		return m, nil
	}
	reg, err := ai.NewRegistry(resources.GlobalDir())
	if err == nil {
		m.s.Registry = reg
		m.s.Agent.APIKey = reg.APIKey
	}
	m.note("saved key for " + provider + " in " + resources.GlobalDir() + "/auth.json")
	return m, nil
}
