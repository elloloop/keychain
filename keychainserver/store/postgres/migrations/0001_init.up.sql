-- Workspaces: top-level tenant boundary.
CREATE TABLE workspaces (
    workspace_id        TEXT        PRIMARY KEY,
    name                TEXT        NOT NULL,
    owner_principal_id  TEXT        NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,
    metadata            JSONB       NOT NULL DEFAULT '{}'::jsonb
);

-- Apis: logical products inside a workspace. ON DELETE CASCADE so removing
-- a workspace removes everything it owns; this matches the contract spec
-- (workspaces are the tenant boundary).
CREATE TABLE apis (
    api_id              TEXT        PRIMARY KEY,
    workspace_id        TEXT        NOT NULL REFERENCES workspaces(workspace_id) ON DELETE CASCADE,
    name                TEXT        NOT NULL,
    key_prefix          TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,
    metadata            JSONB       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX apis_workspace_id_idx ON apis(workspace_id);

-- Keys: the issued bearer credentials. key_hash is sha256(plaintext);
-- plaintext is never stored. UNIQUE on key_hash makes the verify lookup
-- a single indexed equality probe and rejects accidental hash collisions
-- (cosmic-ray odds, but the constraint costs nothing).
CREATE TABLE keys (
    key_id              TEXT        PRIMARY KEY,
    api_id              TEXT        NOT NULL REFERENCES apis(api_id)             ON DELETE CASCADE,
    workspace_id        TEXT        NOT NULL REFERENCES workspaces(workspace_id) ON DELETE CASCADE,
    owner_principal_id  TEXT        NOT NULL,
    name                TEXT        NOT NULL DEFAULT '',
    key_hash            BYTEA       NOT NULL,
    permissions         TEXT[]      NOT NULL DEFAULT '{}'::text[],
    limit_refs          JSONB       NOT NULL DEFAULT '[]'::jsonb,
    expires_at          TIMESTAMPTZ,
    remaining_uses      BIGINT      NOT NULL DEFAULT -1,
    enabled             BOOLEAN     NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,
    last_verified_at    TIMESTAMPTZ,
    metadata            JSONB       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT keys_key_hash_unique UNIQUE (key_hash)
);
CREATE INDEX keys_api_id_idx             ON keys(api_id);
CREATE INDEX keys_owner_principal_id_idx ON keys(owner_principal_id);
