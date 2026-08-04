CREATE TABLE schema_migration (
  id           TEXT PRIMARY KEY,
  checksum     TEXT NOT NULL,
  time_applied INTEGER NOT NULL
);

CREATE TABLE event_sequence (
  aggregate_id TEXT PRIMARY KEY,
  seq          INTEGER NOT NULL,
  owner_id     TEXT
);

CREATE TABLE event (
  id           TEXT PRIMARY KEY,
  aggregate_id TEXT NOT NULL,
  seq          INTEGER NOT NULL,
  type         TEXT NOT NULL,
  data         TEXT NOT NULL,
  FOREIGN KEY (aggregate_id)
    REFERENCES event_sequence(aggregate_id)
    ON DELETE CASCADE
);

CREATE UNIQUE INDEX event_aggregate_seq_idx
  ON event(aggregate_id, seq);

CREATE INDEX event_aggregate_type_seq_idx
  ON event(aggregate_id, type, seq);
