CREATE TABLE prompt_requests (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 request_key TEXT NOT NULL,
 payload_fingerprint TEXT NOT NULL,
 run_id TEXT NOT NULL UNIQUE,
 accepted_at TEXT NOT NULL,
 PRIMARY KEY(session_id, request_key),
 CHECK(length(trim(request_key)) > 0 AND length(CAST(request_key AS BLOB)) <= 256),
 CHECK(length(payload_fingerprint) > 0),
 CHECK(length(run_id) > 0)
);
CREATE INDEX prompt_requests_run ON prompt_requests(run_id);
