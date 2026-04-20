-- +goose Up
-- `rationale_tag` is a short enum that labels *why* a finding is non-obvious
-- (Phase 2 #5, the "why non-obvious" chip). It is NOT the category or the
-- human message — those already exist. Rationale is about signalling depth:
-- this finding was worth flagging because it caught an implicit assumption,
-- a conflict across specs, a missing negative path, etc.
--
-- Nullable because not every finding has a meaningful rationale. CHECK
-- enforces the enum so the LLM cannot smuggle free text.

ALTER TABLE findings
    ADD COLUMN rationale_tag TEXT
        CHECK (rationale_tag IS NULL OR rationale_tag IN (
            'assumption_gap',
            'spec_conflict',
            'missing_negative',
            'unstated_behavior',
            'weak_precondition'
        ));

-- +goose Down
ALTER TABLE findings DROP COLUMN IF EXISTS rationale_tag;
