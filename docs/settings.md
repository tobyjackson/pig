# Settings

Settings live in `~/.pig/settings.json` (global) and `.pig/settings.json`
(project; overrides global, loaded only when the project is trusted). All
keys are optional.

```json
{
  "defaultProvider": "anthropic",
  "defaultModel": "claude-opus-5",
  "defaultThinkingLevel": "high",
  "defaultProjectTrust": "ask",
  "hideThinkingBlock": false,
  "quietStartup": false,
  "shellPath": "/bin/bash",
  "sessionDir": "~/.pig/sessions",
  "steeringMode": "one-at-a-time",
  "followUpMode": "one-at-a-time",
  "compaction": { "enabled": true, "reserveTokens": 16384, "keepRecentTokens": 20000 },
  "retry": { "enabled": true, "maxRetries": 3 },
  "skills": ["~/work/skills"],
  "prompts": ["~/work/prompts"],
  "extensions": ["~/work/ext/lint.py"]
}
```

| key | default | meaning |
| --- | --- | --- |
| `defaultProvider`, `defaultModel` | first model with a key | model at startup. `Ctrl+S` in the model picker saves these. |
| `defaultThinkingLevel` | `high` | off, minimal, low, medium, high, xhigh, max |
| `defaultProjectTrust` | `ask` | what to do with a folder's `.pig` files when nothing is saved: `ask`, `always`, `never`. Non-interactive modes cannot ask, so `ask` acts like `never` there. |
| `hideThinkingBlock` | false | start with thinking hidden (`Ctrl+T` toggles) |
| `quietStartup` | false | skip the startup summary |
| `shellPath` | `bash` | shell used by the bash tool and `!` commands |
| `sessionDir` | `~/.pig/sessions` | where session files go |
| `steeringMode` | `one-at-a-time` | `all` delivers every queued steering message at once |
| `followUpMode` | `one-at-a-time` | same for follow-ups |
| `compaction.enabled` | true | compact automatically when close to the context limit |
| `compaction.reserveTokens` | 16384 | room kept free for the reply |
| `compaction.keepRecentTokens` | 20000 | recent history kept word for word |
| `retry.maxRetries` | 3 | retries on rate limits and server errors |
| `skills`, `prompts`, `extensions` | [] | extra files or folders to load |

## Environment variables

| variable | meaning |
| --- | --- |
| `PIG_DIR` | replaces `~/.pig` |
| `PIG_SESSION_DIR` | replaces the sessions folder |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY` | keys for the built-in providers |
| `VISUAL`, `EDITOR` | editor for `Ctrl+G` |

Commands run by the bash tool see `PIG_SESSION_ID`, `PIG_SESSION_FILE`,
`PIG_PROVIDER`, `PIG_MODEL`, `PIG_REASONING_LEVEL`, and `PIG_CODING_AGENT=true`.

## Trust

`~/.pig/trust.json` remembers, per folder, whether its `.pig` files may be
loaded. `pig -a` trusts for one run, `pig -na` ignores them, `/trust` saves
a yes.
