--
-- PostgreSQL database dump
--

\restrict shcRX78Mp2RmENfeSk19HXUr9TaCxYdAxl6haLR52hxzabUpJuLpbE7J9WfHg58

-- Dumped from database version 16.10
-- Dumped by pg_dump version 16.10

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: public; Type: SCHEMA; Schema: -; Owner: -
--

-- *not* creating schema, since initdb creates it


--
-- Name: citext; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public;


--
-- Name: DatabaseStatus; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."DatabaseStatus" AS ENUM (
    'PROVISIONING',
    'READY',
    'FAILED'
);


--
-- Name: DatabaseType; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."DatabaseType" AS ENUM (
    'POSTGRES',
    'MYSQL',
    'REDIS',
    'MONGO'
);


--
-- Name: DeploymentBranchRole; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."DeploymentBranchRole" AS ENUM (
    'SOURCE',
    'MERGE'
);


--
-- Name: DeploymentStageStatus; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."DeploymentStageStatus" AS ENUM (
    'PENDING',
    'RUNNING',
    'SUCCEEDED',
    'FAILED',
    'SKIPPED',
    'ROLLED_BACK'
);


--
-- Name: DeploymentStatus; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."DeploymentStatus" AS ENUM (
    'QUEUED',
    'CLONING',
    'BUILDING',
    'PUSHING',
    'STARTING',
    'RUNNING',
    'SUCCEEDED',
    'FAILED',
    'CANCELLED'
);


--
-- Name: EnvironmentType; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."EnvironmentType" AS ENUM (
    'PRODUCTION',
    'PREVIEW',
    'MANUAL'
);


--
-- Name: GitCredentialKind; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."GitCredentialKind" AS ENUM (
    'SSH_KEY',
    'PAT',
    'GITHUB_APP_INSTALL'
);


--
-- Name: IdentityProvider; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."IdentityProvider" AS ENUM (
    'PASSWORD',
    'GITHUB',
    'GOOGLE',
    'ATLASSIAN'
);


--
-- Name: OrgInvitationStatus; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."OrgInvitationStatus" AS ENUM (
    'PENDING',
    'ACCEPTED',
    'DECLINED',
    'REVOKED',
    'EXPIRED'
);


--
-- Name: OrgRole; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."OrgRole" AS ENUM (
    'OWNER',
    'ADMIN',
    'MEMBER',
    'VIEWER'
);


--
-- Name: SnapshotStatus; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."SnapshotStatus" AS ENUM (
    'PENDING',
    'READY',
    'FAILED'
);


--
-- Name: UserRole; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public."UserRole" AS ENUM (
    'USER',
    'ADMIN'
);


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: _prisma_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public._prisma_migrations (
    id character varying(36) NOT NULL,
    checksum character varying(64) NOT NULL,
    finished_at timestamp with time zone,
    migration_name character varying(255) NOT NULL,
    logs text,
    rolled_back_at timestamp with time zone,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    applied_steps_count integer DEFAULT 0 NOT NULL
);


--
-- Name: audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_log (
    id bigint NOT NULL,
    actor_user_id uuid,
    action text NOT NULL,
    resource_type text,
    resource_id uuid,
    before_json jsonb,
    after_json jsonb,
    ip inet,
    user_agent text,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: audit_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: audit_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.audit_log_id_seq OWNED BY public.audit_log.id;


--
-- Name: database_snapshots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.database_snapshots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    database_id uuid NOT NULL,
    status public."SnapshotStatus" DEFAULT 'PENDING'::public."SnapshotStatus" NOT NULL,
    storage_key text,
    size_bytes bigint,
    error_summary text,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: deployment_branches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.deployment_branches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    deployment_id uuid NOT NULL,
    role public."DeploymentBranchRole" NOT NULL,
    name text NOT NULL,
    sha text,
    ordinal integer NOT NULL
);


--
-- Name: deployment_stages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.deployment_stages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    deployment_id uuid NOT NULL,
    name text NOT NULL,
    status public."DeploymentStageStatus" DEFAULT 'PENDING'::public."DeploymentStageStatus" NOT NULL,
    ordinal integer NOT NULL,
    started_at timestamp(3) with time zone,
    finished_at timestamp(3) with time zone,
    error_json jsonb,
    attempt integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 1 NOT NULL
);


--
-- Name: deployments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.deployments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    environment_id uuid NOT NULL,
    triggered_by_user_id uuid,
    status public."DeploymentStatus" DEFAULT 'QUEUED'::public."DeploymentStatus" NOT NULL,
    commit_sha text,
    commit_message text,
    commit_author text,
    started_at timestamp(3) with time zone,
    finished_at timestamp(3) with time zone,
    last_heartbeat_at timestamp(3) with time zone,
    error_summary text,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    cancel_requested_at timestamp(3) with time zone,
    framework text
);


--
-- Name: environments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.environments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    project_id uuid NOT NULL,
    name text NOT NULL,
    type public."EnvironmentType" NOT NULL,
    branch text,
    subdomain text NOT NULL,
    custom_domain text,
    env_vars_enc text,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) with time zone NOT NULL,
    pr_number integer
);


--
-- Name: event_outbox; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.event_outbox (
    id bigint NOT NULL,
    type text NOT NULL,
    user_id uuid,
    payload jsonb NOT NULL,
    drained_at timestamp(3) with time zone,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: event_outbox_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.event_outbox_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: event_outbox_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.event_outbox_id_seq OWNED BY public.event_outbox.id;


--
-- Name: git_credentials; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.git_credentials (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    kind public."GitCredentialKind" NOT NULL,
    label text,
    secret_enc text NOT NULL,
    fingerprint text,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) with time zone NOT NULL
);


--
-- Name: managed_databases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.managed_databases (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    environment_id uuid NOT NULL,
    type public."DatabaseType" NOT NULL,
    version text NOT NULL,
    name text NOT NULL,
    status public."DatabaseStatus" DEFAULT 'PROVISIONING'::public."DatabaseStatus" NOT NULL,
    host text,
    port integer,
    conn_secret_enc text,
    error_summary text,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) with time zone NOT NULL,
    owns_container boolean DEFAULT true NOT NULL,
    run_migrate boolean DEFAULT false NOT NULL,
    seed_from_database_id uuid,
    seed_from_snapshot_id uuid
);


--
-- Name: managed_files; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.managed_files (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    environment_id uuid NOT NULL,
    path text NOT NULL,
    content_enc text NOT NULL,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) with time zone NOT NULL
);


--
-- Name: org_invitations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.org_invitations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    org_id uuid NOT NULL,
    role public."OrgRole" NOT NULL,
    invited_by_user_id uuid NOT NULL,
    invitee_user_id uuid,
    token_hash text,
    email text,
    status public."OrgInvitationStatus" DEFAULT 'PENDING'::public."OrgInvitationStatus" NOT NULL,
    expires_at timestamp(3) with time zone NOT NULL,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: org_memberships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.org_memberships (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    org_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role public."OrgRole" NOT NULL,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: organizations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.organizations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    is_personal boolean DEFAULT false NOT NULL,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) with time zone NOT NULL
);


--
-- Name: projects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.projects (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    subdomain text NOT NULL,
    repo_url text NOT NULL,
    default_branch text DEFAULT 'main'::text NOT NULL,
    build_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) with time zone NOT NULL,
    deleted_at timestamp(3) with time zone,
    provider_repo_full_name text,
    webhook_secret_enc text,
    organization_id uuid NOT NULL,
    created_by_user_id uuid NOT NULL
);


--
-- Name: services; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.services (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    deployment_id uuid NOT NULL,
    name text NOT NULL,
    image text,
    container_id text,
    host text,
    port integer,
    is_web boolean DEFAULT false NOT NULL,
    status text
);


--
-- Name: user_identities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    provider public."IdentityProvider" NOT NULL,
    provider_user_id text NOT NULL,
    provider_email text,
    access_token_enc text,
    refresh_token_enc text,
    token_expires_at timestamp(3) with time zone,
    scopes text[],
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) with time zone NOT NULL
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email public.citext NOT NULL,
    email_verified_at timestamp(3) with time zone,
    password_hash text,
    display_name text,
    role public."UserRole" DEFAULT 'USER'::public."UserRole" NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    last_login_at timestamp(3) with time zone,
    created_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp(3) with time zone NOT NULL,
    username public.citext NOT NULL
);


--
-- Name: webhook_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook_deliveries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider text NOT NULL,
    delivery_id text NOT NULL,
    event_type text NOT NULL,
    project_id uuid,
    received_at timestamp(3) with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: audit_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_log ALTER COLUMN id SET DEFAULT nextval('public.audit_log_id_seq'::regclass);


--
-- Name: event_outbox id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_outbox ALTER COLUMN id SET DEFAULT nextval('public.event_outbox_id_seq'::regclass);


--
-- Name: _prisma_migrations _prisma_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public._prisma_migrations
    ADD CONSTRAINT _prisma_migrations_pkey PRIMARY KEY (id);


--
-- Name: audit_log audit_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_log
    ADD CONSTRAINT audit_log_pkey PRIMARY KEY (id);


--
-- Name: database_snapshots database_snapshots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.database_snapshots
    ADD CONSTRAINT database_snapshots_pkey PRIMARY KEY (id);


--
-- Name: deployment_branches deployment_branches_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deployment_branches
    ADD CONSTRAINT deployment_branches_pkey PRIMARY KEY (id);


--
-- Name: deployment_stages deployment_stages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deployment_stages
    ADD CONSTRAINT deployment_stages_pkey PRIMARY KEY (id);


--
-- Name: deployments deployments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deployments
    ADD CONSTRAINT deployments_pkey PRIMARY KEY (id);


--
-- Name: environments environments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environments
    ADD CONSTRAINT environments_pkey PRIMARY KEY (id);


--
-- Name: event_outbox event_outbox_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_outbox
    ADD CONSTRAINT event_outbox_pkey PRIMARY KEY (id);


--
-- Name: git_credentials git_credentials_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.git_credentials
    ADD CONSTRAINT git_credentials_pkey PRIMARY KEY (id);


--
-- Name: managed_databases managed_databases_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.managed_databases
    ADD CONSTRAINT managed_databases_pkey PRIMARY KEY (id);


--
-- Name: managed_files managed_files_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.managed_files
    ADD CONSTRAINT managed_files_pkey PRIMARY KEY (id);


--
-- Name: org_invitations org_invitations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_invitations
    ADD CONSTRAINT org_invitations_pkey PRIMARY KEY (id);


--
-- Name: org_memberships org_memberships_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_memberships
    ADD CONSTRAINT org_memberships_pkey PRIMARY KEY (id);


--
-- Name: organizations organizations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.organizations
    ADD CONSTRAINT organizations_pkey PRIMARY KEY (id);


--
-- Name: projects projects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_pkey PRIMARY KEY (id);


--
-- Name: services services_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_pkey PRIMARY KEY (id);


--
-- Name: user_identities user_identities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_identities
    ADD CONSTRAINT user_identities_pkey PRIMARY KEY (id);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: webhook_deliveries webhook_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_pkey PRIMARY KEY (id);


--
-- Name: audit_log_actor_user_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX audit_log_actor_user_id_created_at_idx ON public.audit_log USING btree (actor_user_id, created_at DESC);


--
-- Name: database_snapshots_database_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX database_snapshots_database_id_created_at_idx ON public.database_snapshots USING btree (database_id, created_at DESC);


--
-- Name: deployment_branches_deployment_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX deployment_branches_deployment_id_idx ON public.deployment_branches USING btree (deployment_id);


--
-- Name: deployment_branches_deployment_id_ordinal_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX deployment_branches_deployment_id_ordinal_key ON public.deployment_branches USING btree (deployment_id, ordinal);


--
-- Name: deployment_stages_deployment_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX deployment_stages_deployment_id_idx ON public.deployment_stages USING btree (deployment_id);


--
-- Name: deployment_stages_deployment_id_ordinal_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX deployment_stages_deployment_id_ordinal_key ON public.deployment_stages USING btree (deployment_id, ordinal);


--
-- Name: deployments_environment_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX deployments_environment_id_created_at_idx ON public.deployments USING btree (environment_id, created_at DESC);


--
-- Name: deployments_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX deployments_status_idx ON public.deployments USING btree (status) WHERE (status = ANY (ARRAY['QUEUED'::public."DeploymentStatus", 'CLONING'::public."DeploymentStatus", 'BUILDING'::public."DeploymentStatus", 'PUSHING'::public."DeploymentStatus", 'STARTING'::public."DeploymentStatus"]));


--
-- Name: environments_project_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX environments_project_id_idx ON public.environments USING btree (project_id);


--
-- Name: environments_project_id_name_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX environments_project_id_name_key ON public.environments USING btree (project_id, name);


--
-- Name: event_outbox_drained_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX event_outbox_drained_at_idx ON public.event_outbox USING btree (drained_at) WHERE (drained_at IS NULL);


--
-- Name: git_credentials_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX git_credentials_user_id_idx ON public.git_credentials USING btree (user_id);


--
-- Name: managed_databases_environment_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX managed_databases_environment_id_idx ON public.managed_databases USING btree (environment_id);


--
-- Name: managed_databases_environment_id_name_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX managed_databases_environment_id_name_key ON public.managed_databases USING btree (environment_id, name);


--
-- Name: managed_files_environment_id_path_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX managed_files_environment_id_path_key ON public.managed_files USING btree (environment_id, path);


--
-- Name: org_invitations_invitee_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX org_invitations_invitee_user_id_idx ON public.org_invitations USING btree (invitee_user_id);


--
-- Name: org_invitations_org_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX org_invitations_org_id_idx ON public.org_invitations USING btree (org_id);


--
-- Name: org_invitations_pending_invitee_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX org_invitations_pending_invitee_key ON public.org_invitations USING btree (org_id, invitee_user_id) WHERE ((status = 'PENDING'::public."OrgInvitationStatus") AND (invitee_user_id IS NOT NULL));


--
-- Name: org_invitations_token_hash_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX org_invitations_token_hash_key ON public.org_invitations USING btree (token_hash);


--
-- Name: org_memberships_org_id_user_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX org_memberships_org_id_user_id_key ON public.org_memberships USING btree (org_id, user_id);


--
-- Name: org_memberships_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX org_memberships_user_id_idx ON public.org_memberships USING btree (user_id);


--
-- Name: organizations_slug_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX organizations_slug_key ON public.organizations USING btree (slug);


--
-- Name: projects_created_by_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX projects_created_by_user_id_idx ON public.projects USING btree (created_by_user_id);


--
-- Name: projects_created_by_user_id_slug_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX projects_created_by_user_id_slug_key ON public.projects USING btree (created_by_user_id, slug);


--
-- Name: projects_created_by_user_id_subdomain_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX projects_created_by_user_id_subdomain_key ON public.projects USING btree (created_by_user_id, subdomain);


--
-- Name: projects_organization_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX projects_organization_id_idx ON public.projects USING btree (organization_id);


--
-- Name: projects_provider_repo_full_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX projects_provider_repo_full_name_idx ON public.projects USING btree (provider_repo_full_name);


--
-- Name: services_deployment_id_name_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX services_deployment_id_name_key ON public.services USING btree (deployment_id, name);


--
-- Name: user_identities_provider_provider_user_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX user_identities_provider_provider_user_id_key ON public.user_identities USING btree (provider, provider_user_id);


--
-- Name: user_identities_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_identities_user_id_idx ON public.user_identities USING btree (user_id);


--
-- Name: users_email_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX users_email_key ON public.users USING btree (email);


--
-- Name: users_username_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX users_username_key ON public.users USING btree (username);


--
-- Name: webhook_deliveries_project_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_deliveries_project_id_idx ON public.webhook_deliveries USING btree (project_id);


--
-- Name: webhook_deliveries_provider_delivery_id_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX webhook_deliveries_provider_delivery_id_key ON public.webhook_deliveries USING btree (provider, delivery_id);


--
-- Name: audit_log audit_log_actor_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_log
    ADD CONSTRAINT audit_log_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: database_snapshots database_snapshots_database_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.database_snapshots
    ADD CONSTRAINT database_snapshots_database_id_fkey FOREIGN KEY (database_id) REFERENCES public.managed_databases(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: deployment_branches deployment_branches_deployment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deployment_branches
    ADD CONSTRAINT deployment_branches_deployment_id_fkey FOREIGN KEY (deployment_id) REFERENCES public.deployments(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: deployment_stages deployment_stages_deployment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deployment_stages
    ADD CONSTRAINT deployment_stages_deployment_id_fkey FOREIGN KEY (deployment_id) REFERENCES public.deployments(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: deployments deployments_environment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deployments
    ADD CONSTRAINT deployments_environment_id_fkey FOREIGN KEY (environment_id) REFERENCES public.environments(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: deployments deployments_triggered_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.deployments
    ADD CONSTRAINT deployments_triggered_by_user_id_fkey FOREIGN KEY (triggered_by_user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: environments environments_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.environments
    ADD CONSTRAINT environments_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: event_outbox event_outbox_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_outbox
    ADD CONSTRAINT event_outbox_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: git_credentials git_credentials_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.git_credentials
    ADD CONSTRAINT git_credentials_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: managed_databases managed_databases_environment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.managed_databases
    ADD CONSTRAINT managed_databases_environment_id_fkey FOREIGN KEY (environment_id) REFERENCES public.environments(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: managed_databases managed_databases_seed_from_database_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.managed_databases
    ADD CONSTRAINT managed_databases_seed_from_database_id_fkey FOREIGN KEY (seed_from_database_id) REFERENCES public.managed_databases(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: managed_databases managed_databases_seed_from_snapshot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.managed_databases
    ADD CONSTRAINT managed_databases_seed_from_snapshot_id_fkey FOREIGN KEY (seed_from_snapshot_id) REFERENCES public.database_snapshots(id) ON UPDATE CASCADE ON DELETE SET NULL;


--
-- Name: managed_files managed_files_environment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.managed_files
    ADD CONSTRAINT managed_files_environment_id_fkey FOREIGN KEY (environment_id) REFERENCES public.environments(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: org_invitations org_invitations_invited_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_invitations
    ADD CONSTRAINT org_invitations_invited_by_user_id_fkey FOREIGN KEY (invited_by_user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE RESTRICT;


--
-- Name: org_invitations org_invitations_invitee_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_invitations
    ADD CONSTRAINT org_invitations_invitee_user_id_fkey FOREIGN KEY (invitee_user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: org_invitations org_invitations_org_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_invitations
    ADD CONSTRAINT org_invitations_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.organizations(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: org_memberships org_memberships_org_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_memberships
    ADD CONSTRAINT org_memberships_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.organizations(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: org_memberships org_memberships_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_memberships
    ADD CONSTRAINT org_memberships_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: projects projects_created_by_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_created_by_user_id_fkey FOREIGN KEY (created_by_user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE RESTRICT;


--
-- Name: projects projects_organization_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_organization_id_fkey FOREIGN KEY (organization_id) REFERENCES public.organizations(id) ON UPDATE CASCADE ON DELETE RESTRICT;


--
-- Name: services services_deployment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_deployment_id_fkey FOREIGN KEY (deployment_id) REFERENCES public.deployments(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- Name: user_identities user_identities_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_identities
    ADD CONSTRAINT user_identities_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--

\unrestrict shcRX78Mp2RmENfeSk19HXUr9TaCxYdAxl6haLR52hxzabUpJuLpbE7J9WfHg58

