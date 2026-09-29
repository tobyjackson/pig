#!/usr/bin/env python3
"""A tiny pig extension. It adds one tool, one command, and blocks `rm -rf`.

pig starts this program and talks to it over stdin/stdout, one JSON object
per line. Copy this file into ~/.pig/extensions/ and make it executable.
"""
import json
import sys


def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


for line in sys.stdin:
    msg = json.loads(line)
    kind = msg.get("type")

    if kind == "init":
        # Tell pig what we offer and which events we want to hear about.
        send({
            "type": "ready",
            "name": "hello",
            "tools": [{
                "name": "shout",
                "description": "Return the text in upper case.",
                "parameters": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]},
            }],
            "commands": [{"name": "hello", "description": "Say hello from the extension"}],
            "events": ["tool_call", "session_start"],
        })

    elif kind == "tool_call":
        text = msg["input"].get("text", "")
        send({"type": "tool_result", "id": msg["id"], "content": [{"type": "text", "text": text.upper()}]})

    elif kind == "command":
        # "message" is sent to the model as the user; "notify" is shown to the user.
        send({"type": "response", "id": msg["id"], "data": {"notify": "hello from the extension, args: " + msg.get("args", "")}})

    elif kind == "event":
        data = msg.get("data", {})
        reply = {}
        if msg["event"] == "tool_call" and data.get("toolName") == "bash":
            if "rm -rf" in data.get("input", {}).get("command", ""):
                reply = {"block": True, "reason": "rm -rf is not allowed by the hello extension"}
        if msg["event"] == "session_start":
            send({"type": "notify", "message": "hello extension loaded", "level": "info"})
        send({"type": "response", "id": msg["id"], "data": reply})

    elif kind == "shutdown":
        break
