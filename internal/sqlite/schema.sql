-- Schema version 1. Text maps (names, cues, summaries) are JSON objects keyed by language, e.g. {"de":"Aufwärmen"}.

CREATE TABLE person (
    id       INTEGER PRIMARY KEY,
    key      TEXT NOT NULL UNIQUE,
    name     TEXT NOT NULL,
    language TEXT NOT NULL DEFAULT 'de'
);

-- Library of exercises, shared by all plans. kind: exercise | rest | breath
CREATE TABLE exercise (
    id       INTEGER PRIMARY KEY,
    key      TEXT NOT NULL UNIQUE,
    kind     TEXT NOT NULL DEFAULT 'exercise' CHECK (kind IN ('exercise', 'rest', 'breath')),
    names    TEXT NOT NULL DEFAULT '{}',
    cues     TEXT NOT NULL DEFAULT '{}',
    variants TEXT NOT NULL DEFAULT '{}' -- {"knees":{"de":"auf den Knien"}}
);

CREATE TABLE plan (
    id         INTEGER PRIMARY KEY,
    key        TEXT NOT NULL UNIQUE,
    created_by TEXT NOT NULL DEFAULT ''
);

-- A saved version never changes; an edit creates version + 1.
CREATE TABLE plan_version (
    id         INTEGER PRIMARY KEY,
    plan_id    INTEGER NOT NULL REFERENCES plan(id),
    version    INTEGER NOT NULL,
    names      TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    hash       TEXT NOT NULL,
    UNIQUE (plan_id, version)
);

-- A block is one item in the exercise list and one segment of the sun ring
-- (e.g. "Wandsitzen" = 25 s + 10 s pause + 25 s).
CREATE TABLE block (
    id              INTEGER PRIMARY KEY,
    plan_version_id INTEGER NOT NULL REFERENCES plan_version(id),
    position        INTEGER NOT NULL,
    names           TEXT NOT NULL DEFAULT '{}',
    summaries       TEXT NOT NULL DEFAULT '{}',
    UNIQUE (plan_version_id, position)
);

CREATE TABLE step (
    id           INTEGER PRIMARY KEY,
    block_id     INTEGER NOT NULL REFERENCES block(id),
    position     INTEGER NOT NULL,
    exercise_id  INTEGER NOT NULL REFERENCES exercise(id),
    duration_sec INTEGER NOT NULL CHECK (duration_sec > 0),
    variant      TEXT NOT NULL DEFAULT '',
    names        TEXT NOT NULL DEFAULT '{}', -- optional override, e.g. "Wandsitzen 1 von 2"
    cues         TEXT NOT NULL DEFAULT '{}', -- optional override
    UNIQUE (block_id, position)
);

-- Which plan a person follows from which day. progression 'latest' = always the newest version.
CREATE TABLE assignment (
    id          INTEGER PRIMARY KEY,
    person_id   INTEGER NOT NULL REFERENCES person(id),
    plan_id     INTEGER NOT NULL REFERENCES plan(id),
    valid_from  TEXT NOT NULL,
    valid_to    TEXT,
    progression TEXT NOT NULL DEFAULT 'latest'
);

CREATE TABLE round (
    id              INTEGER PRIMARY KEY,
    person_id       INTEGER NOT NULL REFERENCES person(id),
    plan_version_id INTEGER NOT NULL REFERENCES plan_version(id),
    date            TEXT NOT NULL, -- local date of the person, YYYY-MM-DD
    started_at      TEXT NOT NULL DEFAULT '',
    recorded_at     TEXT NOT NULL,
    completed       INTEGER NOT NULL DEFAULT 1,
    skipped_steps   TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX round_person_date ON round(person_id, date);
