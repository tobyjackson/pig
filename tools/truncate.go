package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Output limits. Whichever is hit first wins.
const (
	MaxLines = 2000
	MaxBytes = 50 * 1024
)

// Truncation describes what was cut from a tool's output.
type Truncation struct {
	Content     string `json:"content"`
	Truncated   bool   `json:"truncated"`
	TruncatedBy string `json:"truncatedBy,omitempty"` // "lines" or "bytes"
	TotalLines  int    `json:"totalLines"`
	TotalBytes  int    `json:"totalBytes"`
	OutputLines int    `json:"outputLines"`
	OutputBytes int    `json:"outputBytes"`
	// True when the very first line is already over the byte limit.
	FirstLineExceedsLimit bool `json:"firstLineExceedsLimit,omitempty"`
	// True when the tail cut had to split a single huge line.
	LastLinePartial bool `json:"lastLinePartial,omitempty"`
}

// FormatSize prints bytes as B, KB, or MB.
func FormatSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if strings.HasSuffix(s, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TruncateHead keeps the first lines that fit. Good for file reads.
func TruncateHead(s string) Truncation {
	lines := splitLines(s)
	t := Truncation{Content: s, TotalLines: len(lines), TotalBytes: len(s), OutputLines: len(lines), OutputBytes: len(s)}
	if len(lines) <= MaxLines && len(s) <= MaxBytes {
		return t
	}
	var out []string
	bytes := 0
	for i, line := range lines {
		if i >= MaxLines {
			t.TruncatedBy = "lines"
			break
		}
		add := len(line) + 1
		if bytes+add > MaxBytes {
			t.TruncatedBy = "bytes"
			if i == 0 {
				t.FirstLineExceedsLimit = true
			}
			break
		}
		out = append(out, line)
		bytes += add
	}
	if t.TruncatedBy == "" {
		t.TruncatedBy = "bytes"
	}
	t.Truncated = true
	t.Content = strings.Join(out, "\n")
	t.OutputLines = len(out)
	t.OutputBytes = len(t.Content)
	return t
}

// tailBytes returns the last n bytes of s, moved forward to the next rune
// boundary so the result is valid UTF-8. A cut inside a multi-byte rune would
// otherwise reach the model as U+FFFD replacement characters.
//
// At most utf8.UTFMax-1 bytes are dropped: a valid rune is never longer than
// that, so a line that is still invalid afterwards was never valid UTF-8 to
// begin with (a binary file), and there is no boundary to find.
func tailBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for i := 0; i < utf8.UTFMax-1 && len(s) > 0 && !utf8.ValidString(s); i++ {
		_, size := utf8.DecodeRuneInString(s)
		s = s[size:]
	}
	return s
}

// TruncateTail keeps the last lines that fit. Good for command output.
func TruncateTail(s string) Truncation {
	lines := splitLines(s)
	t := Truncation{Content: s, TotalLines: len(lines), TotalBytes: len(s), OutputLines: len(lines), OutputBytes: len(s)}
	if len(lines) <= MaxLines && len(s) <= MaxBytes {
		return t
	}
	var out []string
	bytes := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if len(out) >= MaxLines {
			t.TruncatedBy = "lines"
			break
		}
		add := len(lines[i]) + 1
		if bytes+add > MaxBytes {
			t.TruncatedBy = "bytes"
			if len(out) == 0 {
				// One giant line: keep its tail bytes, cut on a rune boundary so
				// the model is not handed mangled text.
				line := lines[i]
				out = append(out, tailBytes(line, MaxBytes))
				t.LastLinePartial = true
			}
			break
		}
		out = append([]string{lines[i]}, out...)
		bytes += add
	}
	if t.TruncatedBy == "" {
		t.TruncatedBy = "bytes"
	}
	t.Truncated = true
	t.Content = strings.Join(out, "\n")
	t.OutputLines = len(out)
	t.OutputBytes = len(t.Content)
	return t
}
