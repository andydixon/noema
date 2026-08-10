-- Contextual source reliability (review pass §7.2 item 14). Reliability is
-- recorded per (source family, domain) from prediction outcomes and operator
-- corrections; a source's standing in one domain never moves its standing
-- in another, and the trust-level credibility itself is never rewritten.
CREATE TABLE source_reliability (
  mind_id    uuid NOT NULL REFERENCES minds(id),
  source     text NOT NULL,             -- evidence family, e.g. actor:andy, source:prometheus:prod
  domain     text NOT NULL,             -- belief subject the outcome concerned
  correct    integer NOT NULL DEFAULT 0,
  wrong      integer NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (mind_id, source, domain)
);
