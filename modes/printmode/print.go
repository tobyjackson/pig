// Package printmode runs one prompt without a UI: plain text output
// (-p) or one JSON event per line (--mode json).
package printmode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/tobyjackson/pig/runtime"
)

// Run sends each prompt in turn. When jsonMode is true every event is
// printed as a JSON line; otherwise only the final assistant text.
func Run(s *runtime.Session, prompts []string, jsonMode bool, out io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	enc := json.NewEncoder(out)
	if jsonMode {
		_ = enc.Encode(s.Store.Header)
		s.Subscribe(func(e runtime.Event) { _ = enc.Encode(e) })
	}
	if len(prompts) == 0 {
		// Read the prompt from stdin when none was given.
		data, _ := io.ReadAll(os.Stdin)
		if strings.TrimSpace(string(data)) == "" {
			return fmt.Errorf("no prompt given")
		}
		prompts = []string{string(data)}
	}
	var lastErr error
	for _, p := range prompts {
		text, handled, err := s.ExpandInput(p)
		if err != nil {
			return err
		}
		if handled && strings.TrimSpace(text) == "" {
			continue
		}
		if err := s.Prompt(ctx, text); err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
		}
	}
	if !jsonMode {
		fmt.Fprintln(out, strings.TrimSpace(s.LastAssistantText()))
	}
	return lastErr
}
