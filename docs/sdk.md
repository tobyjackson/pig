# Using pig from Go

The `runtime` package is the same code the command line uses.

```go
import "github.com/tobyjackson/pig/runtime"

s, err := runtime.New(runtime.Options{
    Cwd:       "/path/to/project", // default: current directory
    Model:     "claude-opus-5",   // default: settings, then first model with a key
    Ephemeral: true,               // do not write a session file
})
if err != nil { /* no key, bad model, ... */ }
defer s.Close()

s.Subscribe(func(e runtime.Event) {
    if e.Type == "message_update" && e.Update != nil && e.Update.Type == "text_delta" {
        fmt.Print(e.Update.Delta)
    }
})

err = s.Prompt(context.Background(), "What files are in this folder?")
fmt.Println(s.LastAssistantText())
```

## Options

The fields of `runtime.Options` match the command-line flags: `Model`,
`ThinkingLevel`, `SessionPath`, `Continue`, `ForkPath`, `Ephemeral`,
`Tools`, `ExcludeTools`, `NoTools`, `SystemPrompt`, `AppendSystemPrompt`,
`NoSkills`, `NoPrompts`, `NoExtensions`, `NoContextFiles`, `SkillPaths`,
`PromptPaths`, `ExtensionPaths`, `Trust` (`"yes"`/`"no"`), and `Stream` to
replace the network call (handy in tests).

## Session methods

- `Prompt(ctx, text)`, `PromptMessage(ctx, msg)` – send and wait. If a run
  is active the text is queued as steering instead.
- `Steer(text)`, `FollowUp(text)`, `Abort()`, `IsRunning()`
- `Subscribe(fn) func()` – events; the returned function unsubscribes.
- `Model()`, `SetModel(m)`, `CycleModel(back)`, `ThinkingLevel()`,
  `SetThinkingLevel(l)`, `CycleThinkingLevel()`
- `Messages()`, `LastAssistantText()`, `Stats()`
- `Compact(ctx, hint)`, `SetAutoCompaction(on)`
- `NewSession()`, `SwitchSession(path)`, `Fork(entryID)`, `Clone()`,
  `NavigateTree(entryID)`, `ForkPoints()`, `Sessions()`, `SetName(n)`
- `Commands()`, `ExpandInput(text)` – resolve `/skill:x`, `/template`, and
  extension commands to the text to send
- `RunBash(ctx, cmd, onData)`, `RecordBash(...)`
- `Reload()`, `Close()`

Fields: `Agent` (the loop), `Store` (the session file), `Registry`
(models), `Skills`, `Prompts`, `ContextFiles`, `Exts`, `Settings`.

## Lower layers

- `ai` – message types and providers. `ai.Stream(ctx, model, context,
  options)` returns a channel of events for one request.
- `agent` – the loop with tools, steering and hooks; no files or sessions.
- `tools` – read, write, edit, bash. Implement `tools.Tool` to add one.
- `session` – the JSONL tree store.
- `compaction` – token estimates and summarising.

See `examples/sdk/main.go` for a complete program.
