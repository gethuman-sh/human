# Dev Containers

Spins up an isolated, reproducible container from your project's `devcontainer.json`, giving an AI agent a clean sandbox to build and run code without touching your machine.

- Starts a container from devcontainer.json
- Builds images from Dockerfiles or features
- Reuses or rebuilds containers as config changes
- Runs lifecycle setup commands automatically
- Names containers consistently per project
- Mounts your project source into the container
- Connects the container to the human daemon
- Copies the host's cross-built linux `human` to `/usr/local/bin/human` after creating the container and before starting it, so the agent inside runs the daemon's own build. It is copied, not bind-mounted: Docker Desktop on macOS shares only a fixed list of host directories and turns anything else into an empty directory, while the daemon reads its own install directory unconditionally. On a host with no cross-built binary — installed via Homebrew or `install.sh` — the daemon downloads the linux archive for its OWN version from the GitHub release, checksum-verifies it and caches it at `~/.human/bin/human-<version>-linux-<arch>`, so later launches at that version need no network and a daemon upgrade never serves the previous version's cache. A launch with no usable `human-linux-<arch>` and nothing downloadable fails before the container is created rather than creating one that cannot start.
- Runs commands inside the running container
- Mounts project-declared cache volumes into every container it creates: the `caches:` section of `.humanconfig` names persistent Docker volumes (`human-cache-<name>`) and their container paths, so consecutive agent runs build warm — explicit opt-in per project, any ecosystem is a config entry, invalid entries degrade to a cold start with a warning, cleanup via `docker volume rm human-cache-<name>`
