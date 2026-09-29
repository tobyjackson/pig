package ai

import (
	"bufio"
	"io"
	"strings"
)

// sseEvent is one server-sent event: an optional name and its data lines joined.
type sseEvent struct {
	Name string
	Data string
}

// readSSE calls fn for each event until the stream ends or fn returns false.
func readSSE(r io.Reader, fn func(sseEvent) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var ev sseEvent
	var data []string
	flush := func() bool {
		if len(data) == 0 && ev.Name == "" {
			return true
		}
		ev.Data = strings.Join(data, "\n")
		ok := fn(ev)
		ev = sseEvent{}
		data = nil
		return ok
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			if !flush() {
				return nil
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			ev.Name = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	flush()
	return sc.Err()
}
