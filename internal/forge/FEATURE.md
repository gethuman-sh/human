# Pull Requests

Lets `human` open pull requests on code-hosting platforms, keeping that work separate from issue tracking. It recognises which of your configured backends can actually host code and only offers pull-request actions where they make sense.

- Opens a pull request from one branch to another
- Sets the title and description of a PR
- Returns the new pull request number and URL
- Knows which backends can host pull requests
- Matches your git remote to the right forge
- Parses HTTPS, SSH, and scp-style remote URLs
