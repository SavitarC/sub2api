-- Add replacement constraints without scanning the existing tables. The old
-- constraints stay active until the replacements have been validated.
SET LOCAL lock_timeout = '5s';

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'users_signup_source_check_feishu'
          AND conrelid = 'users'::regclass
    ) THEN
        ALTER TABLE users
            ADD CONSTRAINT users_signup_source_check_feishu
            CHECK (signup_source IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google', 'dingtalk', 'feishu'))
            NOT VALID;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_identities_provider_type_check_feishu'
          AND conrelid = 'auth_identities'::regclass
    ) THEN
        ALTER TABLE auth_identities
            ADD CONSTRAINT auth_identities_provider_type_check_feishu
            CHECK (provider_type IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google', 'dingtalk', 'feishu'))
            NOT VALID;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_identity_channels_provider_type_check_feishu'
          AND conrelid = 'auth_identity_channels'::regclass
    ) THEN
        ALTER TABLE auth_identity_channels
            ADD CONSTRAINT auth_identity_channels_provider_type_check_feishu
            CHECK (provider_type IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google', 'dingtalk', 'feishu'))
            NOT VALID;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'pending_auth_sessions_provider_type_check_feishu'
          AND conrelid = 'pending_auth_sessions'::regclass
    ) THEN
        ALTER TABLE pending_auth_sessions
            ADD CONSTRAINT pending_auth_sessions_provider_type_check_feishu
            CHECK (provider_type IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google', 'dingtalk', 'feishu'))
            NOT VALID;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'user_provider_default_grants_provider_type_check_feishu'
          AND conrelid = 'user_provider_default_grants'::regclass
    ) THEN
        ALTER TABLE user_provider_default_grants
            ADD CONSTRAINT user_provider_default_grants_provider_type_check_feishu
            CHECK (provider_type IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google', 'dingtalk', 'feishu'))
            NOT VALID;
    END IF;
END $$;
