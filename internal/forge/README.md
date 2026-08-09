# Pull Requests

Lets `human` open pull requests on code-hosting platforms, keeping that work separate from issue tracking — separate all the way down, not just in name. A code host is configured in its own `forges:` section, loaded here, and carried as its own type. It shares nothing with a tracker, so no code anywhere has to ask which of the two it is holding ([SC-3876]).

- Opens a pull request from one branch to another
- Sets the title and description of a PR
- Returns the new pull request number and URL
- Reads a pull request's combined CI verdict (check runs and legacy statuses)
- Merges a pull request into its base branch
- Deletes the source branch after a merge
- Lists what you have configured (`human forge list`), which is also where an empty answer explains that pull requests cannot be opened at all
- Migrates a config that predates the split (`human config migrate`): a `githubs:` entry that used to double as the code host gains a `forges:` entry beside it, carrying the same token — as a vault reference when that is what it was, never resolved into the file
- Matches your git remote to the right forge
- Parses HTTPS, SSH, and scp-style remote URLs
