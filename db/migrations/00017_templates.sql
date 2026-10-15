-- Templates with immutable, numbered versions, and the template reference of
-- emails sent from one.

-- +goose Up
CREATE TABLE templates (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    project_id     uuid        NOT NULL REFERENCES projects (id),
    slug           text        NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name           text        NOT NULL DEFAULT '' CHECK (char_length(name) <= 200),
    latest_version integer     NOT NULL DEFAULT 0 CHECK (latest_version >= 0),
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug),
    UNIQUE (id, project_id)
);

CREATE INDEX templates_project_idx ON templates (project_id, id DESC);

CREATE TABLE template_versions (
    template_id uuid        NOT NULL,
    project_id  uuid        NOT NULL,
    version     integer     NOT NULL CHECK (version >= 1),
    subject     text        NOT NULL CHECK (octet_length(subject) BETWEEN 1 AND 1000),
    text_body   text        NOT NULL CHECK (octet_length(text_body) BETWEEN 1 AND 524288),
    html_body   text        CHECK (octet_length(html_body) BETWEEN 1 AND 524288),
    variables   jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(variables) = 'object'),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (template_id, version),
    UNIQUE (template_id, project_id, version),
    FOREIGN KEY (template_id, project_id) REFERENCES templates (id, project_id)
);

-- A published version never changes: emails and clients refer to it.
-- +goose StatementBegin
CREATE FUNCTION template_versions_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'template versions are immutable' USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER template_versions_no_change BEFORE UPDATE OR DELETE ON template_versions
    FOR EACH ROW EXECUTE FUNCTION template_versions_immutable();
CREATE TRIGGER template_versions_no_truncate BEFORE TRUNCATE ON template_versions
    FOR EACH STATEMENT EXECUTE FUNCTION template_versions_immutable();

ALTER TABLE emails
    ADD COLUMN template_id uuid,
    ADD COLUMN template_version integer,
    ADD CONSTRAINT emails_template_pair_check CHECK ((template_id IS NULL) = (template_version IS NULL)),
    ADD CONSTRAINT emails_template_fk FOREIGN KEY (template_id, project_id, template_version)
        REFERENCES template_versions (template_id, project_id, version);

CREATE INDEX emails_project_template_idx ON emails (project_id, template_id, id DESC) WHERE template_id IS NOT NULL;

-- +goose Down
DROP INDEX emails_project_template_idx;
ALTER TABLE emails
    DROP CONSTRAINT emails_template_fk,
    DROP CONSTRAINT emails_template_pair_check,
    DROP COLUMN template_version,
    DROP COLUMN template_id;
DROP TABLE template_versions;
DROP FUNCTION template_versions_immutable();
DROP TABLE templates;
