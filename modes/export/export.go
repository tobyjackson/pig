// Package export writes a session as a standalone HTML page.
package export

import (
	"fmt"
	"html"
	"os"
	"strings"

	"github.com/tobyjackson/pig/ai"
)

// HTML writes msgs to path with minimal styling that works offline.
func HTML(path, title string, msgs []ai.Message) error {
	var sb strings.Builder
	sb.WriteString("<!doctype html><meta charset=utf-8><title>" + html.EscapeString(title) + "</title>")
	sb.WriteString("<style>body{font:15px/1.5 system-ui,sans-serif;max-width:900px;margin:2rem auto;padding:0 1rem;color:#222}" +
		".m{margin:1rem 0;padding:.75rem 1rem;border-radius:8px;white-space:pre-wrap;word-break:break-word}" +
		".user{background:#e8f0fe}.assistant{background:#f6f6f6}.tool{background:#fff8e1;font-family:ui-monospace,monospace;font-size:13px}" +
		".think{color:#666;font-style:italic}.role{font-size:12px;text-transform:uppercase;color:#888;margin-bottom:.25rem}</style>")
	sb.WriteString("<h1>" + html.EscapeString(title) + "</h1>")
	for _, m := range msgs {
		switch m.Role {
		case "user":
			sb.WriteString("<div class='m user'><div class=role>user</div>" + html.EscapeString(m.TextContent()) + "</div>")
		case "assistant":
			sb.WriteString("<div class='m assistant'><div class=role>assistant · " + html.EscapeString(m.Model) + "</div>")
			for _, c := range m.Content {
				switch c.Type {
				case "thinking":
					if c.Thinking != "" {
						sb.WriteString("<div class=think>" + html.EscapeString(c.Thinking) + "</div>")
					}
				case "text":
					sb.WriteString(html.EscapeString(c.Text))
				case "toolCall":
					sb.WriteString(fmt.Sprintf("<div class='m tool'>→ %s %s</div>", html.EscapeString(c.Name), html.EscapeString(string(c.Arguments))))
				}
			}
			sb.WriteString("</div>")
		case "toolResult":
			sb.WriteString("<div class='m tool'><div class=role>" + html.EscapeString(m.ToolName) + " result</div>" + html.EscapeString(m.TextContent()) + "</div>")
		}
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}
