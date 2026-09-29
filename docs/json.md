# JSON mode

`pig --mode json "your prompt"` runs the prompt and prints every event as
one JSON object per line, then exits. Good for logging or piping into other
tools.

The first line is the session header:

```json
{"type":"session","version":1,"id":"...","timestamp":"...","cwd":"/path"}
```

Then events, in order:

| type | fields | when |
| --- | --- | --- |
| `agent_start` | | the run begins |
| `turn_start` | | before each model call |
| `message_start` | `message` | the model starts replying |
| `message_update` | `message`, `assistantMessageEvent` | a piece of the reply: `text_delta`, `thinking_delta`, `toolcall_start`, `toolcall_end`, … |
| `message_end` | `message` | the reply is complete |
| `tool_execution_start` | `toolCallId`, `toolName`, `args` | a tool begins |
| `tool_execution_update` | `toolCallId`, `partialResult` | live output (bash) |
| `tool_execution_end` | `toolCallId`, `result`, `isError` | a tool finished |
| `turn_end` | `message`, `toolResults` | the turn is over |
| `queue_update` | `steering`, `followUp` | queued messages changed |
| `compaction_start` / `compaction_end` | `reason`, `summary` or `error` | compaction ran |
| `auto_retry` | `attempt`, `maxAttempts`, `delayMs`, `error` | retrying a failed request |
| `notify` | `text`, `level` | a note from an extension or pig |
| `agent_end` | `messages` | the run is over; `messages` are those added by this run |

Example, extracting only the reply text:

```sh
pig --mode json "say hi" | jq -r 'select(.type=="message_update") | .assistantMessageEvent | select(.type=="text_delta") | .delta' | tr -d '\n'
```
