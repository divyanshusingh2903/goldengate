# ChatGPT export fixtures

Synthetic. The real export layout is not formally documented, so these follow the commonly seen shape and were NOT produced from a real export. Replace with a scrubbed real sample when available.

- `export/`: a single `conversations.json` with an edited-prompt branch (only the branch ending at `current_node` is visible), hidden/system/thoughts/code/tool/multimodal/profile messages, a ChatGPT Project (`g-p-...`) and a custom GPT, a conversation without `current_node`, a cyclic mapping, malformed elements, plus decoys the adapter must never open (`user.json`, `chat.html`, `message_feedback.json`).
- `sharded/`: `conversations-000.json` and `conversations-001.json`.

`SECRET-*` / `PRIVATE*` / `INJECTED*` / `OLD BRANCH` strings mark content that must never reach a session. Tests zip the directories on the fly.
