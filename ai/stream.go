package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 0}

// Options tune one request.
type Options struct {
	APIKey        string
	ThinkingLevel string
	MaxTokens     int
	MaxRetries    int // retries on 429/5xx/network before any output; default 3
	OnRetry       func(attempt, max int, delay time.Duration, err error)
}

// HTTPError is a non-200 reply from a provider.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	body := e.Body
	if len(body) > 400 {
		body = body[:400] + "..."
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, strings.TrimSpace(body))
}

// Retryable reports whether an error is worth trying again.
func Retryable(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == 429 || he.Status == 408 || he.Status == 409 || he.Status >= 500
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), "EOF")
}

// StreamFunc is the shape of any model call: events arrive on the channel,
// and the channel closes when the request ends. Errors are sent as an
// "error" event carrying an assistant message with StopReason error/aborted.
type StreamFunc func(ctx context.Context, m Model, c Context, opts Options) <-chan Event

// Stream calls the right provider for the model, with retries for transient
// failures that happen before any content has streamed.
func Stream(ctx context.Context, m Model, c Context, opts Options) <-chan Event {
	out := make(chan Event, 64)
	go func() {
		defer close(out)
		maxRetries := opts.MaxRetries
		if maxRetries == 0 {
			maxRetries = 3
		}
		for attempt := 0; ; attempt++ {
			// Events pass through as they arrive. dispatch returns an error
			// before any event when the request itself fails, so a retry
			// never shows the caller two starts.
			buf := make(chan Event, 64)
			done := make(chan error, 1)
			go func() { done <- dispatch(ctx, m, c, opts, buf); close(buf) }()
			started := false
			for ev := range buf {
				started = true
				out <- ev
			}
			err := <-done
			if !started && err != nil && Retryable(err) && attempt < maxRetries && ctx.Err() == nil {
				goto retry
			}
			if err != nil {
				reason := StopError
				if ctx.Err() != nil {
					reason = StopAborted
				}
				em := &Message{Role: "assistant", Content: []Content{}, Provider: m.Provider, Model: m.ID, Usage: &Usage{},
					StopReason: reason, ErrorMessage: err.Error(), Timestamp: Now()}
				out <- Event{Type: "error", Reason: reason, Message: em}
			}
			return
		retry:
			delay := retryDelay(attempt)
			if opts.OnRetry != nil {
				opts.OnRetry(attempt+1, maxRetries, delay, err)
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				em := &Message{Role: "assistant", Content: []Content{}, Provider: m.Provider, Model: m.ID, Usage: &Usage{},
					StopReason: StopAborted, ErrorMessage: "aborted", Timestamp: Now()}
				out <- Event{Type: "error", Reason: StopAborted, Message: em}
				return
			}
		}
	}()
	return out
}

func dispatch(ctx context.Context, m Model, c Context, opts Options, out chan<- Event) error {
	switch m.API {
	case "anthropic-messages":
		return anthropicStream(ctx, m, c, opts, out)
	case "openai-completions", "":
		return openaiStream(ctx, m, c, opts, out)
	}
	return fmt.Errorf("unknown api %q for model %s", m.API, m.Key())
}

// retryDelay is 2s, 4s, 8s... capped at 60s. Tests shorten it.
var retryDelay = func(attempt int) time.Duration {
	d := time.Duration(2<<attempt) * time.Second
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}
