# claude.ai export fixtures

Synthetic. The real export layout is not formally documented, so `export/` follows the commonly seen shape (`conversations.json`, `projects.json`, `users.json`) and was NOT produced from a real export. Replace it with a scrubbed real sample when one is available.

`SECRET-*` and `PRIVATE*` strings mark content that must never reach a session, work item or payload: attachment/artifact contents, thinking blocks, project instructions and documents, and everything in `users.json` (which the adapter must never open). Tests zip the directory on the fly.
