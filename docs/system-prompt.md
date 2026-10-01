# The system prompt

pig builds a system prompt for every session: a short persona, the tools it
has, and a few guidelines. That default is hardcoded, so a fresh install
needs no files at all.

Two optional files change it. Both are read at session start.

| file | effect |
| --- | --- |
| `SYSTEM.md` | replaces the default prompt entirely |
| `APPEND_SYSTEM.md` | added after the default, and after `SYSTEM.md` |

Project first, then global: `.pig/SYSTEM.md` beats `~/.pig/SYSTEM.md`, and
`.pig/APPEND_SYSTEM.md` beats `~/.pig/APPEND_SYSTEM.md`. Project files load
only when the folder is trusted. `--system-prompt` and
`--append-system-prompt` do the same for one run.

Neither file is created for you. Write one when you want to change how the
model behaves everywhere.

## Example: a personal appendix

`~/.pig/APPEND_SYSTEM.md` is the usual place for house rules that are yours
rather than the project's. A minimal one:

```markdown
# How to work

Answer briefly. Lead with the problem, not a summary.

Never commit, push, or tag unless asked.

At session start, read `~/.pig/MEMORY.toml`. If it is missing, create it.
After every step, rewrite it before doing anything else, with the same keys
each time: `updated_at`, `task`, `next_step`, `blocked_on`, `where`,
`open_decisions`, `notes_for_next_step`. Keep facts that must survive across
sessions in its body. `TODO.txt` lists work in progress.
```

That last paragraph is how you get memory between sessions without pig
storing anything itself. The model reads the file when it starts and writes
it back as it works. The file is plain text, so you can edit or delete it by
hand.

Use `AGENTS.md` instead when the rules belong to the project and should be
committed. See `pig docs quickstart`.
