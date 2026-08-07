-- Validation scans use SHARE UPDATE EXCLUSIVE rather than the ACCESS EXCLUSIVE
-- lock needed to replace a constraint. Keep these scans in a separate migration
-- transaction so normal reads and writes can continue while large tables scan.
SET LOCAL lock_timeout = '5s';

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'users_signup_source_check_feishu'
          AND conrelid = 'users'::regclass
    ) THEN
        ALTER TABLE users VALIDATE CONSTRAINT users_signup_source_check_feishu;
    END IF;
END $$;

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_identities_provider_type_check_feishu'
          AND conrelid = 'auth_identities'::regclass
    ) THEN
        ALTER TABLE auth_identities VALIDATE CONSTRAINT auth_identities_provider_type_check_feishu;
    END IF;
END $$;

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_identity_channels_provider_type_check_feishu'
          AND conrelid = 'auth_identity_channels'::regclass
    ) THEN
        ALTER TABLE auth_identity_channels VALIDATE CONSTRAINT auth_identity_channels_provider_type_check_feishu;
    END IF;
END $$;

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'pending_auth_sessions_provider_type_check_feishu'
          AND conrelid = 'pending_auth_sessions'::regclass
    ) THEN
        ALTER TABLE pending_auth_sessions VALIDATE CONSTRAINT pending_auth_sessions_provider_type_check_feishu;
    END IF;
END $$;

DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'user_provider_default_grants_provider_type_check_feishu'
          AND conrelid = 'user_provider_default_grants'::regclass
    ) THEN
        ALTER TABLE user_provider_default_grants VALIDATE CONSTRAINT user_provider_default_grants_provider_type_check_feishu;
    END IF;
END $$;
