# Dev Containers

Spins up an isolated, reproducible container from your project's `devcontainer.json`, giving an AI agent a clean sandbox to build and run code without touching your machine.

- Starts a container from devcontainer.json
- Builds images from Dockerfiles or features
- Reuses or rebuilds containers as config changes
- Runs lifecycle setup commands automatically
- Names containers consistently per project
- Mounts your project source into the container
- Connects the container to the human daemon
- Runs commands inside the running container
- Mounts project-declared cache volumes into every container it creates: the `caches:` section of `.humanconfig` names persistent Docker volumes (`human-cache-<name>`) and their container paths, so consecutive agent runs build warm — explicit opt-in per project, any ecosystem is a config entry, invalid entries degrade to a cold start with a warning, cleanup via `docker volume rm human-cache-<name>`
