package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsReservedEmail_DingTalkDomain(t *testing.T) {
	require.True(t, isReservedEmail("dingtalk-123@dingtalk-connect.invalid"))
	require.True(t, isReservedEmail("DINGTALK-456@DINGTALK-CONNECT.INVALID")) // case-insensitive
	require.False(t, isReservedEmail("real@dingtalk.com"))
}

func TestIsReservedEmail_FeishuDomain(t *testing.T) {
	require.True(t, isReservedEmail("feishu-user@feishu-connect.invalid"))
	require.True(t, isReservedEmail("FEISHU-USER@FEISHU-CONNECT.INVALID"))
	require.Equal(t, "feishu", inferLegacySignupSource("feishu-user@feishu-connect.invalid"))
}

func TestNormalizeOAuthSuggestedEmail(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "normalizes", input: "  User@Example.com  ", want: "user@example.com"},
		{name: "rejects display name", input: "User <user@example.com>"},
		{name: "rejects malformed", input: "not-an-email"},
		{name: "rejects header injection", input: "user@example.com\r\nBcc: attacker@example.com"},
		{name: "rejects provider reserved domain", input: "user@feishu-connect.invalid"},
		{name: "rejects any invalid tld", input: "user@another.invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, NormalizeOAuthSuggestedEmail(tt.input))
		})
	}
}
