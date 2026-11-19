package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 假登录凭证：用 CS 自己生成的 OAUTH_ENCRYPTION_KEY 做一遍标准 Fernet
// 加密，写进 ~/.claude-science/.oauth-tokens/<account>.enc。
// 上游实现是 Python cryptography 的 Fernet，这里只依赖标准库，
// 格式（版本字节 / 时间戳 / IV / AES-128-CBC / HMAC-SHA256）与之一致。

const (
	fernetVersion     = 0x80
	fernetKeySize     = 32
	fernetIVSize      = aes.BlockSize
	fernetHMACSize    = sha256.Size
	fernetMinTokenLen = 1 + 8 + fernetIVSize + aes.BlockSize + fernetHMACSize
)

// fernet 是 Fernet key 拆出来的签名/加密两半。
type fernet struct {
	signKey []byte
	encKey  []byte
}

func newFernet(key string) (*fernet, error) {
	raw, err := decodeFernetKey(key)
	if err != nil {
		return nil, err
	}
	if len(raw) != fernetKeySize {
		return nil, fmt.Errorf("encryption key must decode to %d bytes, got %d", fernetKeySize, len(raw))
	}
	return &fernet{
		signKey: raw[:16],
		encKey:  raw[16:],
	}, nil
}

// decodeFernetKey 解析 Fernet key 的值，容忍 CS 生成 key 的几种实际写法：
//   - 值被引号包住（.env 风格 OAUTH_ENCRYPTION_KEY="..."，单双引号都认）
//   - 标准 base64（+ /）与 urlsafe base64（- _）都接受：两个字母表里
//     + ↔ -、/ ↔ _ 表示同一个 6 bit 值，解出的 32 字节完全相同
//   - padding 可省略（Raw 变体）
//
// 四种编码都失败时，错误里带第一个非法字符及其偏移（0-based），
// 而不是只留下一句 "illegal base64 data"。
func decodeFernetKey(key string) ([]byte, error) {
	value := trimKeyQuotes(strings.TrimSpace(key))
	if value == "" {
		return nil, fmt.Errorf("encryption key is empty")
	}
	for _, enc := range []*base64.Encoding{
		base64.URLEncoding,
		base64.RawURLEncoding,
		base64.StdEncoding,
		base64.RawStdEncoding,
	} {
		if raw, err := enc.DecodeString(value); err == nil {
			return raw, nil
		}
	}
	for i := 0; i < len(value); i++ {
		if !isBase64Byte(value[i]) {
			return nil, fmt.Errorf("encryption key has invalid character %q at offset %d (length %d); expected base64 or urlsafe base64",
				value[i], i, len(value))
		}
	}
	return nil, fmt.Errorf("encryption key is not valid base64 (length %d); expected base64 or urlsafe base64", len(value))
}

// trimKeyQuotes 剥掉值两侧成对的引号，避免 `OAUTH_ENCRYPTION_KEY="..."` 这种
// .env 写法把引号当成 base64 内容。
func trimKeyQuotes(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return strings.TrimSpace(value[1 : len(value)-1])
		}
	}
	return value
}

// isBase64Byte 判断字符是否属于标准/urlsafe base64 字母表（含 padding）。
func isBase64Byte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '+', c == '/', c == '-', c == '_', c == '=':
		return true
	}
	return false
}

// encrypt 用当前时间和随机 IV 加密。
func (f *fernet) encrypt(plaintext []byte) (string, error) {
	iv := make([]byte, fernetIVSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", fmt.Errorf("read IV: %w", err)
	}
	return f.encryptAt(plaintext, uint64(time.Now().Unix()), iv)
}

// encryptAt 允许测试注入时间戳与 IV，从而和固定测试向量逐字节比对。
func (f *fernet) encryptAt(plaintext []byte, timestamp uint64, iv []byte) (string, error) {
	if len(iv) != fernetIVSize {
		return "", fmt.Errorf("IV must be %d bytes, got %d", fernetIVSize, len(iv))
	}

	block, err := aes.NewCipher(f.encKey)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	token := make([]byte, 0, 1+8+len(iv)+len(ciphertext)+fernetHMACSize)
	token = append(token, fernetVersion)
	token = binary.BigEndian.AppendUint64(token, timestamp)
	token = append(token, iv...)
	token = append(token, ciphertext...)
	token = append(token, f.sign(token)...)

	return base64.URLEncoding.EncodeToString(token), nil
}

// decrypt 校验 HMAC 后解密。密钥或密文被改动都必须返回错误。
func (f *fernet) decrypt(encoded string) ([]byte, error) {
	token, err := base64.URLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("token is not valid urlsafe base64: %w", err)
	}
	if len(token) < fernetMinTokenLen {
		return nil, fmt.Errorf("token too short: %d bytes", len(token))
	}
	if token[0] != fernetVersion {
		return nil, fmt.Errorf("unsupported fernet version 0x%02x", token[0])
	}

	body, signature := token[:len(token)-fernetHMACSize], token[len(token)-fernetHMACSize:]
	if subtle.ConstantTimeCompare(f.sign(body), signature) != 1 {
		return nil, fmt.Errorf("HMAC verification failed: token or key was modified")
	}

	iv := body[9 : 9+fernetIVSize]
	ciphertext := body[9+fernetIVSize:]
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of %d", len(ciphertext), aes.BlockSize)
	}

	block, err := aes.NewCipher(f.encKey)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	padded := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(padded, ciphertext)
	return pkcs7Unpad(padded, aes.BlockSize)
}

func (f *fernet) sign(data []byte) []byte {
	mac := hmac.New(sha256.New, f.signKey)
	mac.Write(data)
	return mac.Sum(nil)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	return append(data, bytes.Repeat([]byte{byte(padding)}, padding)...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("invalid padded length %d", len(data))
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, fmt.Errorf("invalid padding length %d", padding)
	}
	for _, b := range data[len(data)-padding:] {
		if int(b) != padding {
			return nil, fmt.Errorf("invalid padding bytes")
		}
	}
	return data[:len(data)-padding], nil
}

// oauthOrganization / oauthToken 的字段与顺序对齐上游 setup-token.py，
// 避免 CS 升级后因为字段缺失判定未登录。
type oauthOrganization struct {
	UUID                              string `json:"uuid"`
	Name                              string `json:"name"`
	OrganizationType                  string `json:"organization_type"`
	RateLimitTier                     string `json:"rate_limit_tier"`
	SeatTier                          string `json:"seat_tier"`
	BillingType                       string `json:"billing_type"`
	HasExtraUsageEnabled              bool   `json:"has_extra_usage_enabled"`
	ClaudeAICompletionFeedbackEnabled bool   `json:"claude_ai_completion_feedback_enabled"`
}

type oauthToken struct {
	AccessToken          string            `json:"access_token"`
	RefreshToken         *string           `json:"refresh_token"`
	APIKey               *string           `json:"api_key"`
	TokenExpiresAt       string            `json:"token_expires_at"`
	Provider             string            `json:"provider"`
	Scopes               string            `json:"scopes"`
	Email                string            `json:"email"`
	AccountUUID          string            `json:"account_uuid"`
	SubscriptionType     string            `json:"subscription_type"`
	RateLimitTier        string            `json:"rate_limit_tier"`
	SeatTier             string            `json:"seat_tier"`
	OrgUUID              string            `json:"org_uuid"`
	Organization         oauthOrganization `json:"organization"`
	BillingType          string            `json:"billing_type"`
	HasExtraUsageEnabled bool              `json:"has_extra_usage_enabled"`
}

func newOAuthToken() oauthToken {
	org := oauthOrganization{
		UUID:                              fakeOrgUUID,
		Name:                              "BYOK Organization",
		OrganizationType:                  "claude_max",
		RateLimitTier:                     "tier_5",
		SeatTier:                          "enterprise_usage_based",
		BillingType:                       "api",
		HasExtraUsageEnabled:              true,
		ClaudeAICompletionFeedbackEnabled: false,
	}
	return oauthToken{
		AccessToken:          fakeAccessToken,
		RefreshToken:         nil,
		APIKey:               nil,
		TokenExpiresAt:       "2099-12-31T23:59:59Z",
		Provider:             "claude_ai",
		Scopes:               fakeScopes,
		Email:                "byok@localhost",
		AccountUUID:          fakeAccountUUID,
		SubscriptionType:     "max",
		RateLimitTier:        "tier_5",
		SeatTier:             "enterprise_usage_based",
		OrgUUID:              fakeOrgUUID,
		Organization:         org,
		BillingType:          "api",
		HasExtraUsageEnabled: true,
	}
}

// readOAuthKey 从 encryption.key 里取 OAUTH_ENCRYPTION_KEY= 那一行。
func readOAuthKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "OAUTH_ENCRYPTION_KEY=") {
			key := strings.TrimSpace(strings.TrimPrefix(line, "OAUTH_ENCRYPTION_KEY="))
			if key == "" {
				return "", fmt.Errorf("OAUTH_ENCRYPTION_KEY is empty in %s", path)
			}
			return key, nil
		}
	}
	return "", fmt.Errorf("OAUTH_ENCRYPTION_KEY not found in %s", path)
}

// runSetupToken 是 `api-bridge setup-token` 的实现。路径固定为
// ~/.claude-science/...，与 CS 及上游 setup-token.py 一致。
func runSetupToken(stdout io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	csHome := filepath.Join(home, ".claude-science")
	keyFile := filepath.Join(csHome, "encryption.key")
	tokenDir := filepath.Join(csHome, ".oauth-tokens")

	rawKey, err := readOAuthKey(keyFile)
	if err != nil {
		return err
	}
	f, err := newFernet(rawKey)
	if err != nil {
		return fmt.Errorf("parse OAUTH_ENCRYPTION_KEY: %w", err)
	}

	token := newOAuthToken()
	plaintext, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	encrypted, err := f.encrypt(plaintext)
	if err != nil {
		return fmt.Errorf("encrypt token: %w", err)
	}

	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", tokenDir, err)
	}
	// MkdirAll 不会修正已存在目录的权限，显式收一次。
	if err := os.Chmod(tokenDir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", tokenDir, err)
	}

	tokenPath := filepath.Join(tokenDir, fakeAccountUUID+".enc")
	file, err := os.OpenFile(tokenPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tokenPath, err)
	}
	if _, err := file.WriteString(encrypted); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", tokenPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tokenPath, err)
	}
	// OpenFile 的 mode 受 umask 影响，显式 chmod 保证 0600。
	if err := os.Chmod(tokenPath, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tokenPath, err)
	}

	// 回读并解密自检：写坏了要在这里就失败，而不是等 CS 启动。
	written, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("verify %s: %w", tokenPath, err)
	}
	decrypted, err := f.decrypt(string(written))
	if err != nil {
		return fmt.Errorf("verify %s: %w", tokenPath, err)
	}
	var check oauthToken
	if err := json.Unmarshal(decrypted, &check); err != nil {
		return fmt.Errorf("verify %s: decrypted token is not valid JSON: %w", tokenPath, err)
	}
	if check.AccountUUID != fakeAccountUUID {
		return fmt.Errorf("verify %s: account_uuid is %q, want %q", tokenPath, check.AccountUUID, fakeAccountUUID)
	}

	fmt.Fprintf(stdout, "token written to %s\n", tokenPath)
	fmt.Fprintf(stdout, "verified: token decrypts\n")
	return nil
}
