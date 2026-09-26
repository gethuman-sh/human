## The launch briefing

When the daemon launches a stage it may append a section to this prompt headed
`## Be aware of these before you change anything`: a few lines distilled from
what past machine reviews found in this project — which files drew findings,
which classes of finding recur, and what the fixers did about them. It is
advice, not a gate: nothing is blocked by it, and a launch that carries no
such section simply had nothing recorded.

**Forward it verbatim to every agent you dispatch.** A subagent sees only the
prompt you write for it, never this one. Append the whole section, heading
included, to the end of every dispatch prompt you compose, so the agent that
actually changes the code is the one that reads the briefing. Do not summarise
or shorten it, and do not act on it yourself in place of the agent.
