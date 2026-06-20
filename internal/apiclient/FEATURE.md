# Tracker Connections

The shared engine `human` uses to talk to every issue tracker and forge over the network. It handles signing in, building requests, and turning failures into clear messages, so every backend behaves consistently and safely.

- Authenticates with tokens, basic auth, or headers
- Builds correct request URLs per backend
- Talks to both REST and GraphQL APIs
- Rejects unsafe URLs to prevent attacks
- Keeps secrets out of error messages
- Caps response sizes against bad upstreams
- Applies per-request timeouts
- Produces clear, provider-specific error messages
