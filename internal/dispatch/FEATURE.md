# Message Dispatch

Lets `human` take messages you send over chat and hand them to idle AI agents running in your terminal. It watches a chat channel, picks an available agent, types the task in, and tells you what happened.

- Polls a Telegram chat for new messages
- Hands tasks to idle Claude agents
- Types prompts into the right tmux pane
- Restricts who and which chats can dispatch
- Notifies you back over Telegram or Slack
- Sends notifications to several channels at once
- Caps overly long messages before dispatch
