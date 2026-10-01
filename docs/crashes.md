# Crash logs

If pig dies from a fatal error, it writes a report to
`~/.pig/crashes/<time>-<pid>.log`. A data race, a stack overflow, or a
similar runtime fault cannot be caught and cleaned up: the process stops at
once. The report is the traceback that would otherwise scroll off the
terminal, plus the version, working directory, and arguments.

The file is written only on a crash. A normal exit, including a
command-line error, removes it, so `~/.pig/crashes/` holds only the runs
that actually failed.

Read the newest one:

```sh
ls -t ~/.pig/crashes | head -1
```

The first line of the report names pig and its arguments. The traceback
below it names the frames, innermost first, with the source line of each.
Send that traceback when reporting a crash; it says exactly where pig
faulted. For the fatal errors that print their reason on the line above the
traceback (for example `fatal error: concurrent map read and map write`),
copy that line from the terminal as well.

To capture everything, including the one-line reason, run pig with its
output redirected:

```sh
pig > ~/pig-run.log 2>&1
```

The interactive screen is on an alternate screen and is not part of that
output.
