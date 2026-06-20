# Per-Request Settings

Lets the shared `human` daemon serve many requests at once while keeping each one's settings separate. Your environment values stay isolated and never leak between concurrent commands.

- Keeps each request's settings separate
- Prevents one command's values leaking into another
- Forwards your environment to the daemon safely
- Falls back to normal lookup outside the daemon
- Works the same for direct CLI runs
