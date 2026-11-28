-- The tables the cas postgres kind (stores/cas/postgres) reads and writes, under their
-- default names; a product names its own through Config and applies this DDL (renamed)
-- in its migrations.

-- cas_nodes holds each node once, under the hash of its content. Rows are inserted and
-- never updated.
CREATE TABLE IF NOT EXISTS cas_nodes (
    scope text  NOT NULL,
    hash  bytea NOT NULL,
    body  bytea NOT NULL,
    PRIMARY KEY (scope, hash)
);

-- cas_refs holds each named pointer to a node's hash; the store moves one by
-- compare-and-swap on hash.
CREATE TABLE IF NOT EXISTS cas_refs (
    scope text  NOT NULL,
    name  text  NOT NULL,
    hash  bytea NOT NULL,
    PRIMARY KEY (scope, name)
);
