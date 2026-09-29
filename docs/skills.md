# Skills

A skill is a folder with a `SKILL.md` file. It teaches the model how to do
one kind of task. Only the name and a one-line description go into the
system prompt; the model reads the full file when the task matches. This
keeps the prompt short.

## Where skills live

- `~/.pig/skills/<name>/SKILL.md` – for you, everywhere
- `~/.agents/skills/<name>/SKILL.md` – shared with other tools
- `.pig/skills/<name>/SKILL.md` – for one project (asked to trust first)
- `.agents/skills/<name>/SKILL.md` – same, shared format
- `--skill <path>` on the command line

A plain `.md` file directly inside a skills folder also works if it has
frontmatter.

## Format

```markdown
---
name: release-notes
description: Write release notes from git history for a version tag.
---

# Release notes

1. Run `git log <previous-tag>..<tag> --oneline`.
2. Group changes into Added, Changed, Fixed.
3. Write them to CHANGELOG.md under a heading for the tag.
```

Rules:

- `name` is 1–64 characters: lowercase letters, digits, and hyphens.
- `description` is required, up to 1024 characters. Say *when* to use the
  skill; that is what the model matches on.
- Optional: `disable-model-invocation: true` hides the skill from the model.
  You can still run it yourself with `/skill:name`.

Relative paths in a skill are resolved against the skill's folder, so you
can ship helper scripts next to `SKILL.md`.

## Running a skill by hand

Type `/skill:release-notes v1.2.0`. The skill text is sent to the model
along with your arguments.

## Tips

- Keep skills short and specific. One task each.
- Put long reference material in a second file and tell the model to read
  it only when needed.
- `/reload` picks up new or edited skills without restarting.
