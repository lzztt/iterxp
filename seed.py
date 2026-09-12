#!/usr/bin/env python3
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

os.umask(0o077)
home = Path.home()
context = home / "context.log"
tool = home / "tool.sh"
keys = {name: (home / "token" / name).read_text().strip()
        for name in ("wandb", "github", "typesafe")}
repo = "lzztt/iterxp"
model = os.environ.get("SEED_MODEL", "deepseek-ai/DeepSeek-V4-Pro-0813")
system = """You are IterXP, a self-building agent controlled by /home/admin/seed.py.
Return only the next small Bash step, not a complete solution to the whole task.
Your response is saved as tool.sh and executed; stdout, stderr, and exit code
will appear in the next context so you can choose the following step.
Inspect files through Bash when needed, including seed.py itself.
Output Bash only, without Markdown fences. Output exactly Done when finished.
Credentials are in ~/token; never reveal their values."""

def redact(text):
    for key in keys.values():
        if key:
            text = text.replace(key, "[REDACTED]")
    return text

def append(text):
    with context.open("a") as log:
        log.write(redact(text) + "\n")

def api(url, key, payload=None, headers=()):
    args = ["curl", "--disable", "--fail", "--silent", "--show-error", "--config", "-"]
    for header in headers:
        args.extend(["-H", header])
    with tempfile.NamedTemporaryFile(mode="w", dir=home) as request:
        if payload is not None:
            json.dump(payload, request)
            request.flush()
            args.extend(["-H", "Content-Type: application/json", "--data-binary", "@" + request.name])
        auth = "header = " + json.dumps("Authorization: Bearer " + key) + "\n"
        return json.loads(subprocess.check_output(args + [url], input=auth, text=True))

context.touch()
seen = set()
last_context = ""
next_poll = 0
empty_retry = False
print(f"Watching {context}; polling {repo}.", flush=True)

while True:
    if time.monotonic() >= next_poll:
        page = 1
        while True:
            issues = api(
                f"https://api.github.com/repos/{repo}/issues?state=open&per_page=100&page={page}",
                keys["github"],
            )
            for issue in issues:
                if "pull_request" not in issue and issue["id"] not in seen:
                    append(f"Issue #{issue['number']}: {issue['title']}\n"
                           f"{issue['html_url']}\n{issue['body'] or ''}")
                    seen.add(issue["id"])
            if len(issues) < 100:
                break
            page += 1
        next_poll = time.monotonic() + 30

    text = context.read_text()
    if text == last_context:
        time.sleep(1)
        continue

    response = api(
        "https://api.inference.wandb.ai/v1/chat/completions", keys["wandb"],
        {"model": model, "max_tokens": 16384, "reasoning_effort": "low",
         "messages": [{"role": "system", "content": system},
                      {"role": "user", "content": redact(text)}]},
        headers=("OpenAI-Project: longti/inference",),
    )
    choice = response["choices"][0]
    message = choice["message"]
    content = message.get("content")
    reason = choice.get("finish_reason")
    if reason == "length" or not isinstance(content, str) or not content.strip():
        print("No complete Bash response:", reason,
              "completion_tokens:", (response.get("usage") or {}).get("completion_tokens"), flush=True)
        if empty_retry or reason not in ("length", "stop") or message.get("refusal") or message.get("tool_calls"):
            raise SystemExit("No executable Bash response; stopping without running anything.")
        empty_retry = True
        append("The previous model response contained no complete Bash script. Return just one small next step.")
        continue
    empty_retry = False
    command = content.strip()
    last_context = text
    if command == "Done":
        print("Done; waiting.", flush=True)
        continue

    tool.write_text(command + "\n")
    result = subprocess.run(["bash", str(tool)], capture_output=True, text=True)
    append(f"$ {command}\nstdout:\n{result.stdout}\nstderr:\n{result.stderr}\n"
           f"exit_code: {result.returncode}")
    print(f"Bash exit_code={result.returncode}", flush=True)
