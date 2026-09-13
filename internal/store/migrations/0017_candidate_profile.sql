CREATE TABLE candidate_profile (
 id bigint PRIMARY KEY CHECK (id=1), active_revision_id bigint, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE candidate_profile_revision (
 id bigserial PRIMARY KEY, profile_id bigint NOT NULL REFERENCES candidate_profile(id) ON DELETE RESTRICT,
 revision integer NOT NULL, document jsonb NOT NULL, content_hash text NOT NULL,
 status text NOT NULL CHECK (status IN ('draft','approved')), approved_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(profile_id,revision), UNIQUE(profile_id,id),
 CHECK ((status='approved') = (approved_at IS NOT NULL))
);
ALTER TABLE candidate_profile ADD CONSTRAINT active_profile_revision_fk FOREIGN KEY(id,active_revision_id) REFERENCES candidate_profile_revision(profile_id,id) ON DELETE RESTRICT;
CREATE FUNCTION protect_profile_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'profile revisions cannot be deleted'; END IF;
 IF OLD.status='approved' OR NEW.document IS DISTINCT FROM OLD.document OR NEW.content_hash IS DISTINCT FROM OLD.content_hash OR NEW.revision<>OLD.revision OR NEW.profile_id<>OLD.profile_id OR NEW.id<>OLD.id OR NEW.created_at<>OLD.created_at THEN
  RAISE EXCEPTION 'profile revision content is immutable; create another draft';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER immutable_profile_revision BEFORE UPDATE OR DELETE ON candidate_profile_revision FOR EACH ROW EXECUTE FUNCTION protect_profile_revision();
CREATE FUNCTION require_approved_profile() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.active_revision_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM candidate_profile_revision WHERE id=NEW.active_revision_id AND profile_id=NEW.id AND status='approved') THEN RAISE EXCEPTION 'active profile must be approved'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER approved_profile_head BEFORE INSERT OR UPDATE ON candidate_profile FOR EACH ROW EXECUTE FUNCTION require_approved_profile();
INSERT INTO candidate_profile(id) VALUES(1);
-- Already approved high-level facts only. No activation, technologies, projects,
-- signature, engagement preferences, or template-derived claims are seeded.
WITH seed AS (SELECT '{"identity":"Full-Stack Software Developer","primary_strength":"Backend Engineering","secondary_position":"Full-Stack Development","desired_roles":[{"id":"role-backend","name":"Backend Engineering","aliases":["Backend Engineer","Backend Developer"]},{"id":"role-fullstack","name":"Full-Stack Development","aliases":["Full Stack Developer","Full-Stack Developer","Fullstack Developer"]},{"id":"role-software","name":"Software Engineering","aliases":["Software Engineer","Software Developer"]}],"signature":{}}'::jsonb AS doc)
INSERT INTO candidate_profile_revision(profile_id,revision,document,content_hash,status,approved_at)
 SELECT 1,1,doc,md5(doc::text),'approved',now() FROM seed;
