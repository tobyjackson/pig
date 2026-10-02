# Keys

| key | action |
| --- | --- |
| Enter | send; while the model works, queue a steering message |
| Alt+Enter | queue a follow-up for after the model finishes |
| Ctrl+J | new line in the editor |
| Esc | stop the current run; press twice to open /tree |
| Ctrl+C | clear the editor; twice quits |
| Ctrl+D | quit |
| Ctrl+L | model picker (Ctrl+S inside it saves the default) |
| Ctrl+P | next model with a key |
| Shift+Tab | next thinking level |
| Ctrl+O | expand or collapse tool output and thinking |
| Ctrl+T | show or hide thinking |
| Alt+T | show or hide tool calls; failed tools always stay |
| Ctrl+X | copy the last reply |
| Ctrl+G | edit the prompt in $EDITOR |
| Tab | complete a file path |
| PgUp, PgDn | scroll the work column |
| Ctrl+Up, Ctrl+Down | scroll the chat column |

Editor tricks: `!cmd` runs a shell command and shares the output with the
model; `!!cmd` runs it privately; `/` shows matching commands as you type.

On a screen at least 100 columns wide the transcript splits: what you and
the model said stays on the left, and thinking, tool calls and their output
scroll on the right. Below that width everything shares one column and
PgUp/PgDn scroll it.
