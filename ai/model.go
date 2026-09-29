package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Cost is price per million tokens, in dollars.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// Model is one selectable model and how to reach it.
type Model struct {
	Provider      string            `json:"provider"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	API           string            `json:"api"` // anthropic-messages | openai-completions
	BaseURL       string            `json:"baseUrl"`
	Reasoning     bool              `json:"reasoning"`
	Input         []string          `json:"input"`
	ContextWindow int               `json:"contextWindow"`
	MaxTokens     int               `json:"maxTokens"`
	Cost          Cost              `json:"cost"`
	Headers       map[string]string `json:"headers,omitempty"`
}

// Key returns "provider/id", the form used on the command line.
func (m Model) Key() string { return m.Provider + "/" + m.ID }

// SupportsImages reports whether the model accepts image input.
func (m Model) SupportsImages() bool {
	for _, i := range m.Input {
		if i == "image" {
			return true
		}
	}
	return false
}

// providerConfig is one entry in models.json.
type providerConfig struct {
	BaseURL string            `json:"baseUrl"`
	API     string            `json:"api"`
	APIKey  string            `json:"apiKey"`
	Headers map[string]string `json:"headers"`
	Models  []struct {
		ID            string   `json:"id"`
		Name          string   `json:"name"`
		API           string   `json:"api"`
		Reasoning     bool     `json:"reasoning"`
		Input         []string `json:"input"`
		ContextWindow int      `json:"contextWindow"`
		MaxTokens     int      `json:"maxTokens"`
		Cost          Cost     `json:"cost"`
	} `json:"models"`
}

// Registry holds every known model and how to authenticate to each provider.
type Registry struct {
	Models  []Model
	apiKeys map[string]string // provider -> raw apiKey setting from models.json
	auth    map[string]string // provider -> key from auth.json
	envKeys map[string]string // provider -> env var name
}

// NewRegistry loads built-in models, then models.json and auth.json from dir.
func NewRegistry(dir string) (*Registry, error) {
	r := &Registry{
		apiKeys: map[string]string{},
		auth:    map[string]string{},
		envKeys: map[string]string{"anthropic": "ANTHROPIC_API_KEY", "openai": "OPENAI_API_KEY", "openrouter": "OPENROUTER_API_KEY"},
	}
	r.Models = append(r.Models, builtinModels()...)
	if err := r.loadModelsFile(filepath.Join(dir, "models.json")); err != nil {
		return nil, err
	}
	r.loadAuthFile(filepath.Join(dir, "auth.json"))
	return r, nil
}

func (r *Registry) loadModelsFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file struct {
		Providers map[string]providerConfig `json:"providers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	names := make([]string, 0, len(file.Providers))
	for n := range file.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		p := file.Providers[name]
		if p.APIKey != "" {
			r.apiKeys[name] = p.APIKey
		}
		for _, m := range p.Models {
			api := m.API
			if api == "" {
				api = p.API
			}
			if api == "" {
				api = "openai-completions"
			}
			model := Model{
				Provider: name, ID: m.ID, Name: m.Name, API: api, BaseURL: p.BaseURL,
				Reasoning: m.Reasoning, Input: m.Input, ContextWindow: m.ContextWindow,
				MaxTokens: m.MaxTokens, Cost: m.Cost, Headers: p.Headers,
			}
			if model.Name == "" {
				model.Name = m.ID
			}
			if len(model.Input) == 0 {
				model.Input = []string{"text"}
			}
			if model.ContextWindow == 0 {
				model.ContextWindow = 128000
			}
			if model.MaxTokens == 0 {
				model.MaxTokens = 16384
			}
			r.replaceOrAdd(model)
		}
	}
	return nil
}

func (r *Registry) replaceOrAdd(m Model) {
	for i, e := range r.Models {
		if e.Provider == m.Provider && e.ID == m.ID {
			r.Models[i] = m
			return
		}
	}
	r.Models = append(r.Models, m)
}

func (r *Registry) loadAuthFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var file map[string]struct {
		APIKey string `json:"apiKey"`
	}
	if json.Unmarshal(data, &file) == nil {
		for p, v := range file {
			r.auth[p] = v.APIKey
		}
	}
}

// SaveAuth writes one provider's key into auth.json.
func SaveAuth(dir, provider, key string) error {
	path := filepath.Join(dir, "auth.json")
	file := map[string]map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &file)
	}
	file[provider] = map[string]string{"apiKey": key}
	data, _ := json.MarshalIndent(file, "", "  ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// APIKey resolves a provider's key: models.json setting, auth.json, then env.
// Settings may be "$VAR" or "!command".
func (r *Registry) APIKey(provider string) string {
	if raw, ok := r.apiKeys[provider]; ok {
		if v := resolveValue(raw); v != "" {
			return v
		}
	}
	if v := r.auth[provider]; v != "" {
		return v
	}
	if env := r.envKeys[provider]; env != "" {
		return os.Getenv(env)
	}
	return ""
}

// HasAuth reports whether a key is available for the provider.
func (r *Registry) HasAuth(provider string) bool { return r.APIKey(provider) != "" }

func resolveValue(raw string) string {
	switch {
	case strings.HasPrefix(raw, "$!"):
		return raw[1:]
	case strings.HasPrefix(raw, "$$"):
		return raw[1:]
	case strings.HasPrefix(raw, "!"):
		out, err := exec.Command("sh", "-c", raw[1:]).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	case strings.HasPrefix(raw, "$"):
		return os.Expand(raw, os.Getenv)
	}
	return raw
}

// Providers lists every provider name pig knows: built-in ones plus any
// from models.json.
func (r *Registry) Providers() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range r.Models {
		if !seen[m.Provider] {
			seen[m.Provider] = true
			out = append(out, m.Provider)
		}
	}
	for p := range r.envKeys {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ClosestProvider suggests a known provider for a misspelled name, or "".
func (r *Registry) ClosestProvider(name string) string {
	best, bestDist := "", 3
	for _, p := range r.Providers() {
		if d := editDistance(strings.ToLower(name), p); d < bestDist {
			best, bestDist = p, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// Find matches "provider/id", an exact id, or a case-insensitive substring.
func (r *Registry) Find(pattern string) (Model, bool) {
	if pattern == "" {
		return Model{}, false
	}
	if p, id, ok := strings.Cut(pattern, "/"); ok {
		for _, m := range r.Models {
			if m.Provider == p && m.ID == id {
				return m, true
			}
		}
	}
	for _, m := range r.Models {
		if m.ID == pattern {
			return m, true
		}
	}
	lower := strings.ToLower(pattern)
	for _, m := range r.Models {
		if strings.Contains(strings.ToLower(m.ID), lower) || strings.Contains(strings.ToLower(m.Name), lower) {
			return m, true
		}
	}
	// OpenRouter serves hundreds of models; accept any id under its prefix.
	if id, ok := strings.CutPrefix(pattern, "openrouter/"); ok && strings.Contains(id, "/") {
		return OpenRouterModel(id, id), true
	}
	return Model{}, false
}

// OpenRouterModel describes a model reached through openrouter.ai. Prices
// are unknown up front; the real cost comes back with each response.
func OpenRouterModel(id, name string) Model {
	return Model{
		Provider: "openrouter", ID: id, Name: name, API: "openai-completions",
		BaseURL: "https://openrouter.ai/api/v1", Reasoning: true,
		Input: []string{"text", "image"}, ContextWindow: 200000, MaxTokens: 32000,
		Headers: map[string]string{"HTTP-Referer": "https://github.com/tobyjackson/pig", "X-Title": "pig"},
	}
}

// Available returns models whose provider has a key.
func (r *Registry) Available() []Model {
	var out []Model
	for _, m := range r.Models {
		if r.HasAuth(m.Provider) {
			out = append(out, m)
		}
	}
	return out
}

// Default picks the first available model, preferring Anthropic's Opus.
func (r *Registry) Default() (Model, bool) {
	if m, ok := r.Find("anthropic/claude-opus-5"); ok && r.HasAuth("anthropic") {
		return m, true
	}
	avail := r.Available()
	if len(avail) == 0 {
		return Model{}, false
	}
	return avail[0], true
}

func builtinModels() []Model {
	anth := func(id, name string, in, out float64, ctx int) Model {
		return Model{
			Provider: "anthropic", ID: id, Name: name, API: "anthropic-messages",
			BaseURL: "https://api.anthropic.com", Reasoning: true,
			Input: []string{"text", "image"}, ContextWindow: ctx, MaxTokens: 32000,
			Cost: Cost{Input: in, Output: out, CacheRead: in / 10, CacheWrite: in * 1.25},
		}
	}
	oai := func(id, name string, in, out float64, reasoning bool) Model {
		return Model{
			Provider: "openai", ID: id, Name: name, API: "openai-completions",
			BaseURL: "https://api.openai.com/v1", Reasoning: reasoning,
			Input: []string{"text", "image"}, ContextWindow: 400000, MaxTokens: 32000,
			Cost: Cost{Input: in, Output: out, CacheRead: in / 4},
		}
	}
	return []Model{
		anth("claude-opus-5", "Claude Opus 5", 5, 25, 1000000),
		anth("claude-sonnet-5", "Claude Sonnet 5", 2, 10, 1000000),
		anth("claude-fable-5-1", "Claude Fable 5.1", 10, 50, 1000000),
		anth("claude-opus-4-8", "Claude Opus 4.8", 5, 25, 1000000),
		anth("claude-sonnet-4-6", "Claude Sonnet 4.6", 3, 15, 1000000),
		anth("claude-haiku-4-5", "Claude Haiku 4.5", 1, 5, 200000),
		oai("gpt-5", "GPT-5", 1.25, 10, true),
		oai("gpt-5-mini", "GPT-5 mini", 0.25, 2, true),
		oai("gpt-4.1", "GPT-4.1", 2, 8, false),
		// A few OpenRouter ids as a starting point; `pig models <search>`
		// lists the live catalogue and any openrouter/<vendor>/<model> works.
		OpenRouterModel("anthropic/claude-fable-5.1", "Claude Fable 5.1 via OpenRouter"),
		OpenRouterModel("openai/gpt-6-astra", "GPT-6 Astra via OpenRouter"),
		OpenRouterModel("google/gemini-3.8-flash", "Gemini 3.8 Flash via OpenRouter"),
		OpenRouterModel("deepseek/deepseek-v4.1-flash", "DeepSeek V4.1 Flash via OpenRouter"),
		OpenRouterModel("z-ai/glm-5.3", "GLM 5.3 via OpenRouter"),
		OpenRouterModel("qwen/qwen3.8-max-0902", "Qwen 3.8 Max via OpenRouter"),
	}
}

// ComputeCost fills usage.Cost from the model's price table, unless the
// provider already reported an exact cost.
func ComputeCost(m Model, u *Usage) {
	u.TotalTokens = u.Input + u.Output + u.CacheRead + u.CacheWrite
	if u.Cost > 0 {
		return
	}
	u.Cost = (float64(u.Input)*m.Cost.Input + float64(u.Output)*m.Cost.Output +
		float64(u.CacheRead)*m.Cost.CacheRead + float64(u.CacheWrite)*m.Cost.CacheWrite) / 1e6
}
