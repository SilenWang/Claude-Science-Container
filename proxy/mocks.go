package proxy

// 假登录 / 假账号接口。CS 在无 Anthropic key 时仍需要这些接口保持"已登录"状态。
// 值与上游实现保持一致，避免 CS 升级后字段对不上。

const (
	fakeAccountUUID = "byok-user-000000000000000000"
	fakeOrgUUID     = "org_byok_000000000000"
	fakeAccessToken = "fake-bearer-token-for-proxy"
	fakeScopes      = "user:inference user:file_upload user:profile user:mcp_servers user:plugins"
)

func fakeToken() map[string]any {
	return map[string]any{
		"token_type":    "bearer",
		"access_token":  fakeAccessToken,
		"refresh_token": "fake-refresh-token",
		"expires_in":    999999999,
		"expires_at":    "2099-12-31T23:59:59Z",
		"scope":         fakeScopes,
		"scopes":        fakeScopes,
		"provider":      "claude_ai",
		"account":       fakeAccount(),
		"organization":  fakeOrg(),
	}
}

func fakeAccount() map[string]any {
	return map[string]any{
		"id":             fakeAccountUUID,
		"uuid":           fakeAccountUUID,
		"sub":            fakeAccountUUID,
		"email":          "byok@localhost",
		"email_address":  "byok@localhost",
		"email_verified": true,
		"name":           "BYOK User",
		"display_name":   "BYOK User",
	}
}

func fakeUser() map[string]any {
	account := fakeAccount()
	org := fakeOrg()
	return map[string]any{
		"id":                      fakeAccountUUID,
		"uuid":                    fakeAccountUUID,
		"sub":                     fakeAccountUUID,
		"email":                   "byok@localhost",
		"email_address":           "byok@localhost",
		"email_verified":          true,
		"name":                    "BYOK User",
		"display_name":            "BYOK User",
		"account":                 account,
		"user":                    account,
		"organization":            org,
		"organizations":           []any{org},
		"active_organization":     org,
		"organization_uuid":       fakeOrgUUID,
		"org_uuid":                fakeOrgUUID,
		"enabled_plugins":         []any{},
		"subscription":            map[string]any{"type": "max", "status": "active"},
		"subscription_type":       "max",
		"rate_limit_tier":         "tier_5",
		"seat_tier":               "enterprise_usage_based",
		"billing_type":            "api",
		"has_extra_usage_enabled": true,
	}
}

func fakeOrg() map[string]any {
	return map[string]any{
		"id":                                    fakeOrgUUID,
		"uuid":                                  fakeOrgUUID,
		"name":                                  "BYOK Organization",
		"type":                                  "organization",
		"organization_type":                     "claude_max",
		"status":                                "active",
		"default_role":                          "admin",
		"subscription":                          map[string]any{"type": "max", "status": "active"},
		"rate_limit_tier":                       "tier_5",
		"seat_tier":                             "enterprise_usage_based",
		"billing_type":                          "api",
		"has_extra_usage_enabled":               true,
		"claude_ai_completion_feedback_enabled": false,
	}
}

func fakeOrgList() map[string]any {
	org := fakeOrg()
	out := map[string]any{}
	for k, v := range org {
		out[k] = v
	}
	out["data"] = []any{org}
	out["organizations"] = []any{org}
	out["has_more"] = false
	out["first_id"] = fakeOrgUUID
	out["last_id"] = fakeOrgUUID
	return out
}
