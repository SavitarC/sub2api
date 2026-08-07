-- Once validation has committed, replace each old constraint using metadata-only
-- operations. Guard on the temporary name so rerunning an already-swapped schema
-- is a no-op instead of dropping the canonical constraint.
SET LOCAL lock_timeout = '5s';

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'users_signup_source_check_feishu'
          AND conrelid = 'users'::regclass
    ) THEN
        ALTER TABLE users DROP CONSTRAINT IF EXISTS users_signup_source_check;
        ALTER TABLE users RENAME CONSTRAINT users_signup_source_check_feishu TO users_signup_source_check;
    END IF;
END $$;

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_identities_provider_type_check_feishu'
          AND conrelid = 'auth_identities'::regclass
    ) THEN
        ALTER TABLE auth_identities DROP CONSTRAINT IF EXISTS auth_identities_provider_type_check;
        ALTER TABLE auth_identities RENAME CONSTRAINT auth_identities_provider_type_check_feishu TO auth_identities_provider_type_check;
    END IF;
END $$;

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_identity_channels_provider_type_check_feishu'
          AND conrelid = 'auth_identity_channels'::regclass
    ) THEN
        ALTER TABLE auth_identity_channels DROP CONSTRAINT IF EXISTS auth_identity_channels_provider_type_check;
        ALTER TABLE auth_identity_channels RENAME CONSTRAINT auth_identity_channels_provider_type_check_feishu TO auth_identity_channels_provider_type_check;
    END IF;
END $$;

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'pending_auth_sessions_provider_type_check_feishu'
          AND conrelid = 'pending_auth_sessions'::regclass
    ) THEN
        ALTER TABLE pending_auth_sessions DROP CONSTRAINT IF EXISTS pending_auth_sessions_provider_type_check;
        ALTER TABLE pending_auth_sessions RENAME CONSTRAINT pending_auth_sessions_provider_type_check_feishu TO pending_auth_sessions_provider_type_check;
    END IF;
END $$;

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'user_provider_default_grants_provider_type_check_feishu'
          AND conrelid = 'user_provider_default_grants'::regclass
    ) THEN
        ALTER TABLE user_provider_default_grants DROP CONSTRAINT IF EXISTS user_provider_default_grants_provider_type_check;
        ALTER TABLE user_provider_default_grants RENAME CONSTRAINT user_provider_default_grants_provider_type_check_feishu TO user_provider_default_grants_provider_type_check;
    END IF;
END $$;
