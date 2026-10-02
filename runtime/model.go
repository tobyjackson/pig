package runtime

import (
	"errors"
	"fmt"

	"github.com/tobyjackson/pig/ai"
	"github.com/tobyjackson/pig/resources"
)

// pickModel chooses the startup model: the flag, then the session's last
// model, then the settings default, then the registry default.
func (s *Session) pickModel() error {
	reg := s.Registry
	var m ai.Model
	var ok bool
	if s.Opts.Model != "" {
		m, ok = reg.Find(s.Opts.Model)
		if !ok {
			return fmt.Errorf("no model matches %q (try --list-models)", s.Opts.Model)
		}
	} else if p, id, found := s.Store.LatestModel(); found {
		m, ok = reg.Find(p + "/" + id)
	}
	if !ok && s.Settings.DefaultModel != "" {
		key := s.Settings.DefaultModel
		if s.Settings.DefaultProvider != "" {
			key = s.Settings.DefaultProvider + "/" + key
		}
		m, ok = reg.Find(key)
	}
	if !ok {
		m, ok = reg.Default()
	}
	if !ok {
		return errors.New("no model available: set ANTHROPIC_API_KEY, OPENAI_API_KEY or OPENROUTER_API_KEY, add a key with `pig login <provider>`, or configure ~/.pig/models.json")
	}
	if s.Opts.Stream == nil && !reg.HasAuth(m.Provider) {
		return fmt.Errorf("no API key for %s (model %s): run `pig login %s` or set the provider's environment variable", m.Provider, m.Key(), m.Provider)
	}
	s.Agent.Model = m
	return nil
}

func (s *Session) pickThinking() {
	level := s.Opts.ThinkingLevel
	if level == "" {
		if l, ok := s.Store.LatestThinkingLevel(); ok {
			level = l
		}
	}
	if level == "" {
		level = s.Settings.DefaultThinkingLevel
	}
	if level == "" {
		level = "off"
	}
	if !ai.ValidThinkingLevel(level) {
		level = "off"
	}
	s.Agent.ThinkingLevel = level
}

// Model returns the active model.
func (s *Session) Model() ai.Model { return s.Agent.Model }

// ThinkingLevel returns the active thinking level.
func (s *Session) ThinkingLevel() string { return s.Agent.ThinkingLevel }

// SetModel switches models and records the change.
func (s *Session) SetModel(m ai.Model) {
	if m.Provider == s.Agent.Model.Provider && m.ID == s.Agent.Model.ID {
		return
	}
	s.Agent.Model = m
	s.Store.AppendModelChange(m.Provider, m.ID)
	s.rebuildTools()
	s.emit(Event{Type: "model_change", Text: m.Key()})
}

// SetThinkingLevel changes the level and records it.
func (s *Session) SetThinkingLevel(level string) error {
	if !ai.ValidThinkingLevel(level) {
		return fmt.Errorf("unknown thinking level %q", level)
	}
	if level == s.Agent.ThinkingLevel {
		return nil
	}
	s.Agent.ThinkingLevel = level
	s.Store.AppendThinkingLevel(level)
	s.rebuildTools()
	s.emit(Event{Type: "thinking_level_change", Text: level})
	return nil
}

// CycleModel moves to the next available model.
func (s *Session) CycleModel(backwards bool) ai.Model {
	avail := s.Registry.Available()
	if len(avail) == 0 {
		return s.Agent.Model
	}
	idx := 0
	for i, m := range avail {
		if m.Provider == s.Agent.Model.Provider && m.ID == s.Agent.Model.ID {
			idx = i
		}
	}
	if backwards {
		idx = (idx - 1 + len(avail)) % len(avail)
	} else {
		idx = (idx + 1) % len(avail)
	}
	s.SetModel(avail[idx])
	return avail[idx]
}

// CycleThinkingLevel moves to the next level, wrapping around.
func (s *Session) CycleThinkingLevel() string {
	levels := ai.ThinkingLevels
	if !s.Agent.Model.Reasoning {
		return s.Agent.ThinkingLevel
	}
	idx := 0
	for i, l := range levels {
		if l == s.Agent.ThinkingLevel {
			idx = i
		}
	}
	next := levels[(idx+1)%len(levels)]
	_ = s.SetThinkingLevel(next)
	return next
}

// SaveDefaultModel remembers the current model as the startup default.
func (s *Session) SaveDefaultModel() error {
	return resources.SaveGlobal(func(m map[string]any) {
		m["defaultProvider"] = s.Agent.Model.Provider
		m["defaultModel"] = s.Agent.Model.ID
		m["defaultThinkingLevel"] = s.Agent.ThinkingLevel
	})
}
