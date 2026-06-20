# OAuth Sign-In

Helps `human` complete OAuth sign-in flows by handling the localhost callback that providers redirect you to after you log in. It spots the callback details in a login URL and can point them at a free port on your machine.

- Detects the localhost redirect in a login URL
- Reads the callback port and path
- Captures the OAuth state parameter
- Reroutes the callback to a chosen port
- Preserves all other login URL parameters
