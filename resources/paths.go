// Package resources finds and loads the files that shape a session:
// settings, skills, prompt templates, context files, and trust decisions.
package resources

import (
	"os"
	"path/filepath"
)

// GlobalDir is pig's home folder, ~/.pig by default. PIG_DIR overrides it.
func GlobalDir() string {
	if d := os.Getenv("PIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".pig"
	}
	return filepath.Join(home, ".pig")
}

// ProjectDir is the project-local folder, <cwd>/.pig.
func ProjectDir(cwd string) string { return filepath.Join(cwd, ".pig") }

// SessionsDir is where session files live. PIG_SESSION_DIR overrides it.
func SessionsDir(settings Settings) string {
	if d := os.Getenv("PIG_SESSION_DIR"); d != "" {
		return d
	}
	if settings.SessionDir != "" {
		return expandHome(settings.SessionDir)
	}
	return filepath.Join(GlobalDir(), "sessions")
}

func expandHome(p string) string {
	if len(p) > 1 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
