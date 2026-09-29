# Quickstart

## 1. Give pig a key

pig talks to a model over the network, so it needs an API key.

```sh
export ANTHROPIC_API_KEY=sk-ant-...
# or
pig login anthropic
```

`pig login` saves the key in `~/.pig/auth.json`. `pig --list-models` shows
which models are ready to use (marked with ✓).

## 2. Start it in a project

```sh
cd my-project
pig
```

The screen has three parts: the conversation on top, an editor at the
bottom, and a status line with the model, thinking level, context use, and
cost.

Type a request and press Enter:

> add a --verbose flag to main.go and print each step

pig will read files, run commands, and edit code. Each tool call appears as
a line starting with ▶. Press `Ctrl+O` to expand the output of tools.

## 3. Steer while it works

You can keep typing while the model works.

- `Enter` sends a steering message. It reaches the model after the current
  tool calls finish, so you can correct course.
- `Alt+Enter` queues a follow-up. It is sent once the model is done.
- `Esc` stops the run. Queued messages are kept.

## 4. Useful commands

Type these in the editor:

- `/model` – pick a model. `/model sonnet` switches by name.
- `/thinking high` – how hard the model thinks (off, minimal, low, medium,
  high, xhigh, max).
- `/new` – start a fresh conversation.
- `/resume` – reopen an earlier one.
- `/compact` – shrink the conversation when it gets long.
- `/session` – tokens, cost, and the session file path.
- `/help`, `/hotkeys` – lists of commands and keys.
- `!ls -la` – run a shell command and share the output with the model.
- `!!git status` – run a command without sharing it.

## 5. One-shot use

```sh
pig "explain what ./cmd/server does"
pig -p "list TODO comments" > todos.txt
echo "review this" | pig -p
```

## 6. Project instructions

Put an `AGENTS.md` (or `CLAUDE.md`) file in your project. pig adds it to the
system prompt every time. A global one at `~/.pig/AGENTS.md` applies
everywhere.

## Next

- `pig docs skills` to teach pig repeatable tasks.
- `pig docs extensions` to add tools.
- `pig docs sessions` to learn how conversations are saved.
