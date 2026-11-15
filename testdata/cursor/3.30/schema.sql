-- The real DDL of the tables the adapter reads (Cursor 3.30, globalStorage/state.vscdb), structure only.
CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);
CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);
CREATE TABLE composerHeaders (composerId TEXT PRIMARY KEY, workspaceId TEXT, createdAt INTEGER, lastUpdatedAt INTEGER, isArchived INTEGER, isSubagent INTEGER, recency INTEGER, checkpointAt INTEGER, subagentTypeName TEXT, value TEXT);
CREATE INDEX idx_composerHeaders_0 ON composerHeaders (workspaceId, isSubagent, isArchived, recency);
CREATE INDEX idx_composerHeaders_1 ON composerHeaders (recency, composerId);

-- Decoys: the same database holds sign-in state and per-conversation keys. The adapter must never read them.
INSERT INTO ItemTable VALUES ('cursorAuth/accessToken', 'TOPSECRET-ACCESS-TOKEN');
INSERT INTO ItemTable VALUES ('cursorAuth/refreshToken', 'TOPSECRET-REFRESH-TOKEN');
INSERT INTO ItemTable VALUES ('cursor/localLastActivityClientVersion', '3.30.14');
