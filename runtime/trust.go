package runtime

import "github.com/tobyjackson/pig/resources"

func (s *Session) decideTrust() bool {
	switch s.Opts.Trust {
	case "yes":
		return true
	case "no":
		return false
	}
	switch resources.TrustDecision(s.Opts.Cwd) {
	case "yes":
		return true
	case "no":
		return false
	}
	if !resources.HasProjectFiles(s.Opts.Cwd) {
		return false
	}
	def := resources.LoadSettings(s.Opts.Cwd, false).DefaultProjectTrust
	if s.Opts.TrustPrompt != nil && def == "ask" {
		trusted, remember := s.Opts.TrustPrompt(s.Opts.Cwd)
		if remember {
			_ = resources.SaveTrust(s.Opts.Cwd, trusted)
		}
		return trusted
	}
	return def == "always"
}

// SaveTrustYes records that this project's .pig folder may be loaded.
func (s *Session) SaveTrustYes() error { return resources.SaveTrust(s.Opts.Cwd, true) }
