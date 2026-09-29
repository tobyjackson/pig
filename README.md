# pig

pig is a small coding agent that runs in your terminal, written in Go. It is
a replica of [pi](https://pi.dev): the model gets four tools (read, write,
edit, bash) and a short system prompt. Everything else is added by you,
through skills, prompt templates, and extensions.

## Install

You need Go 1.24 or newer.

```sh
go install github.com/tobyjackson/pig/cmd/pig@latest
```

Or from a clone of this repo:

```sh
go build -o pig ./cmd/pig
```

## First run

Give pig an API key. Either export one:

```sh
export ANTHROPIC_API_KEY=sk-ant-...   # or OPENAI_API_KEY, OPENROUTER_API_KEY
```

or save one with pig:

```sh
pig login anthropic                   # or: pig login openrouter
```

With an [OpenRouter](https://openrouter.ai) key one login covers many
vendors: `pig models <search>` lists what is available and
`pig --model openrouter/<vendor>/<model>` uses it.

Then start it in a project folder:

```sh
cd my-project
pig
```

Type what you want. pig reads files, runs commands, and edits code, showing
each step. Press `Esc` to stop a run, `/help` to list commands, `/hotkeys`
for keys, and `Ctrl+C` twice to quit.

## Ways to run

| Command | What it does |
| --- | --- |
| `pig` | interactive screen |
| `pig "fix the failing test"` | one prompt, prints the reply, exits |
| `pig --mode json "summarise main.go"` | same, but every event as a JSON line |
| `pig --mode rpc` | driven by another program over stdin/stdout |
| `pig -c` | continue the latest session in this folder |
| `pig -r` | pick an older session |
| `pig --list-models` | show the models pig knows about |
| `pig models gemini` | search the live OpenRouter catalogue |
| `pig docs` | read the built-in docs |

Use pig as a Go library too: see `pig docs sdk` and `examples/sdk`.

## Where things live

```
~/.pig/                 pig's home (PIG_DIR overrides it)
  settings.json         defaults such as model and thinking level
  models.json           extra providers and models
  auth.json             saved API keys (written by pig login)
  sessions/             one JSONL file per conversation
  skills/               SKILL.md folders the model can load on demand
  prompts/              markdown templates you run with /name
  extensions/           programs that add tools, commands, and hooks
  AGENTS.md             instructions added to every session
.pig/                   the same folders, per project (asked to trust first)
AGENTS.md, CLAUDE.md    project instructions, picked up automatically
```

## Docs

Run `pig docs <topic>` or open the `docs/` folder:

- `quickstart` – a first session walkthrough
- `skills` – teach the model a task it loads only when needed
- `prompt-templates` – reusable prompts with arguments
- `extensions` – add tools and hooks in any language
- `sessions` – the session file format, branching, forking
- `settings` – every settings.json key
- `models` – add providers with models.json
- `rpc` and `json` – protocols for driving pig from code
- `sdk` – use pig from Go

## Differences from pi

pig follows pi's design closely but is not byte-for-byte compatible:

- Extensions are separate programs speaking JSON lines, not TypeScript
  modules, so they can be written in any language.
- Providers built in: Anthropic, OpenAI, and OpenRouter. Others that speak
  either API go through `models.json`.
- No package manager (`pi install`) yet. Copy files into `~/.pig/`.
- The interactive screen uses Bubble Tea and is simpler than pi's TUI.

## Development

```sh
go test ./...
go run ./internal/fakeserver          # a fake model for trying pig offline
```

MIT licensed.
