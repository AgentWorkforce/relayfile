# Concurrency Rubric

Concurrency cases model multiple agents through ordered operations with explicit
agent labels. The important signal is the final observable mount state and the
content each agent read, not wall-clock stress. A failure means agents can see
stale or divergent views of the same relayfile path.
