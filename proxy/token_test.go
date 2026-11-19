package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 固定测试向量：由独立实现生成（openssl 做 AES-128-CBC，Python hashlib/hmac
// 算 HMAC-SHA256），不是本项目加密再解密自证出来的。
const (
	testFernetKey       = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	testFernetTimestamp = uint64(1700000000)
	testFernetIVHex     = "00112233445566778899aabbccddeeff"
	testFernetPlaintext = `{"access_token":"fake-bearer-token-for-proxy","account_uuid":"byok-user-000000000000000000"}`
	testFernetToken     = "gAAAAABlU_EAABEiM0RVZneImaq7zN3u_xKqDH0vzZdXbJsC8hIk4hKG6o7L4SMxgbuSCfZ3haE44QlDPm4a7s3Mh057PdjPNALMFvZPJ0Is86Y4onlYEqZ8OZu6qR0W8aE6RpI0EJ3sWG8YcIeOh1_3EnFZdLS0YOlznIbYf918Dgg-5l9i0GtiqxQXxDE8sJpKGGMM862x"
)

func testFernet(t *testing.T) *fernet {
	t.Helper()
	f, err := newFernet(testFernetKey)
	if err != nil {
		t.Fatalf("newFernet: %v", err)
	}
	return f
}

func testVectorIV(t *testing.T) []byte {
	t.Helper()
	iv, err := decodeHex(testFernetIVHex)
	if err != nil {
		t.Fatalf("decode test IV: %v", err)
	}
	return iv
}

func decodeHex(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, err := hexNibble(s[2*i])
		if err != nil {
			return nil, err
		}
		lo, err := hexNibble(s[2*i+1])
		if err != nil {
			return nil, err
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	default:
		return 0, &os.PathError{Op: "decode", Path: "hex", Err: os.ErrInvalid}
	}
}

// 解密独立实现生成的 token，得到固定明文。
func TestFernetDecryptsFixedVector(t *testing.T) {
	got, err := testFernet(t).decrypt(testFernetToken)
	if err != nil {
		t.Fatalf("decrypt fixed vector: %v", err)
	}
	if string(got) != testFernetPlaintext {
		t.Fatalf("plaintext mismatch:\n got %q\nwant %q", got, testFernetPlaintext)
	}
}

// 固定时间戳 + 固定 IV 下，加密结果必须与独立实现的 token 逐字节一致。
func TestFernetEncryptMatchesFixedVector(t *testing.T) {
	got, err := testFernet(t).encryptAt([]byte(testFernetPlaintext), testFernetTimestamp, testVectorIV(t))
	if err != nil {
		t.Fatalf("encrypt fixed vector: %v", err)
	}
	if got != testFernetToken {
		t.Fatalf("token mismatch:\n got %s\nwant %s", got, testFernetToken)
	}
}

// 密钥被改动一位：虽然仍能解析，但 HMAC 校验必须失败。
func TestFernetRejectsModifiedKey(t *testing.T) {
	raw, err := base64.URLEncoding.DecodeString(testFernetKey)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	raw[31] ^= 0x01
	modified, err := newFernet(base64.URLEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("newFernet modified: %v", err)
	}
	if _, err := modified.decrypt(testFernetToken); err == nil {
		t.Fatal("decrypt with modified key succeeded, want error")
	}
}

// 密文被改动一位：HMAC 必须先失败（不做任何解密输出）。
func TestFernetRejectsModifiedCiphertext(t *testing.T) {
	raw, err := base64.URLEncoding.DecodeString(testFernetToken)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	// 版本(1) + 时间戳(8) + IV(16) 之后是密文。
	raw[1+8+16] ^= 0x01
	tampered := base64.URLEncoding.EncodeToString(raw)
	if _, err := testFernet(t).decrypt(tampered); err == nil {
		t.Fatal("decrypt with modified ciphertext succeeded, want error")
	}
}

// 签名被改动一位、token 被截断、版本字节不对，都必须失败。
func TestFernetRejectsCorruptTokens(t *testing.T) {
	f := testFernet(t)
	raw, err := base64.URLEncoding.DecodeString(testFernetToken)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}

	badSignature := append([]byte(nil), raw...)
	badSignature[len(badSignature)-1] ^= 0x01

	badVersion := append([]byte(nil), raw...)
	badVersion[0] = 0x81

	cases := map[string]string{
		"modified signature":  base64.URLEncoding.EncodeToString(badSignature),
		"truncated token":     base64.URLEncoding.EncodeToString(raw[:len(raw)-8]),
		"unsupported version": base64.URLEncoding.EncodeToString(badVersion),
		"not base64":          "not-a-token!!",
		"empty":               "",
	}
	for name, token := range cases {
		if _, err := f.decrypt(token); err == nil {
			t.Errorf("%s: decrypt succeeded, want error", name)
		}
	}
}

func writeTestEncryptionKey(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	csHome := filepath.Join(home, ".claude-science")
	if err := os.MkdirAll(csHome, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	keyFile := filepath.Join(csHome, "encryption.key")
	content := "OAUTH_ENCRYPTION_KEY=" + testFernetKey + "\n"
	if err := os.WriteFile(keyFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return csHome
}

// setup-token 端到端：目录 0700、文件 0600、内容可用同一 key 解密、
// 输出包含 verified 行。
func TestSetupTokenWritesDecryptableToken(t *testing.T) {
	csHome := writeTestEncryptionKey(t)
	var stdout bytes.Buffer
	if err := runSetupToken(&stdout); err != nil {
		t.Fatalf("runSetupToken: %v", err)
	}

	tokenDir := filepath.Join(csHome, ".oauth-tokens")
	dirInfo, err := os.Stat(tokenDir)
	if err != nil {
		t.Fatalf("stat token dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("token dir perm = %04o, want 0700", perm)
	}

	tokenPath := filepath.Join(tokenDir, fakeAccountUUID+".enc")
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file perm = %04o, want 0600", perm)
	}

	written, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	plaintext, err := testFernet(t).decrypt(string(written))
	if err != nil {
		t.Fatalf("decrypt written token: %v", err)
	}
	var token oauthToken
	if err := json.Unmarshal(plaintext, &token); err != nil {
		t.Fatalf("unmarshal token: %v", err)
	}
	if token.AccountUUID != fakeAccountUUID {
		t.Errorf("account_uuid = %q, want %q", token.AccountUUID, fakeAccountUUID)
	}
	if token.Provider != "claude_ai" {
		t.Errorf("provider = %q, want claude_ai", token.Provider)
	}
	if token.Organization.UUID != fakeOrgUUID {
		t.Errorf("organization.uuid = %q, want %q", token.Organization.UUID, fakeOrgUUID)
	}
	if !strings.Contains(stdout.String(), "verified: token decrypts") {
		t.Errorf("stdout missing verification line:\n%s", stdout.String())
	}
}

// 没有 encryption.key 时必须报错，而不是写出一份假凭证。
func TestSetupTokenFailsWithoutEncryptionKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runSetupToken(&bytes.Buffer{}); err == nil {
		t.Fatal("runSetupToken succeeded without encryption.key, want error")
	}
}

// encryption.key 里缺 OAUTH_ENCRYPTION_KEY= 行时必须报错。
func TestReadOAuthKeyRejectsMissingVariable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "encryption.key")
	if err := os.WriteFile(path, []byte("SOMETHING_ELSE=value\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if _, err := readOAuthKey(path); err == nil {
		t.Fatal("readOAuthKey succeeded without OAUTH_ENCRYPTION_KEY, want error")
	}
}

// standardBase64TestKey 返回一个 32 字节的 key，以及它的标准 base64
// （含 '+' 与 '/'）和 urlsafe base64 两种写法。CS 实际生成的值就是标准
// base64——用户现场那份以 "a+Lz..." 开头，第 2 个字符即 '+'。
func standardBase64TestKey(t *testing.T) (raw []byte, std, urlsafe string) {
	t.Helper()
	raw = make([]byte, fernetKeySize)
	for i := range raw {
		if i < fernetKeySize/2 {
			raw[i] = 0xfb
		} else {
			raw[i] = 0xff
		}
	}
	std = base64.StdEncoding.EncodeToString(raw)
	urlsafe = base64.URLEncoding.EncodeToString(raw)
	if !strings.ContainsAny(std, "+/") {
		t.Fatalf("test key %q must contain '+' or '/'", std)
	}
	if strings.ContainsAny(urlsafe, "+/") {
		t.Fatalf("urlsafe test key %q must not contain '+' or '/'", urlsafe)
	}
	return raw, std, urlsafe
}

// SIL-952 第 6 条：标准 base64、urlsafe、省 padding、带引号、带首尾空格
// 五种写法都必须解出同一份 32 字节。
func TestNewFernetAcceptsStandardAndQuotedKeys(t *testing.T) {
	raw, std, urlsafe := standardBase64TestKey(t)

	cases := map[string]string{
		"standard":          std,
		"standard raw":      strings.TrimRight(std, "="),
		"urlsafe":           urlsafe,
		"urlsafe raw":       strings.TrimRight(urlsafe, "="),
		"double quoted":     `"` + std + `"`,
		"single quoted":     `'` + std + `'`,
		"surrounding space": "  " + std + "  ",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			f, err := newFernet(key)
			if err != nil {
				t.Fatalf("newFernet(%q): %v", key, err)
			}
			if !bytes.Equal(f.signKey, raw[:16]) || !bytes.Equal(f.encKey, raw[16:]) {
				t.Fatalf("newFernet(%q) decoded different bytes", key)
			}
		})
	}

	// 标准与 urlsafe 只是表示层差异，解出的 Fernet 必须完全一样。
	stdFernet, err := newFernet(std)
	if err != nil {
		t.Fatalf("newFernet(standard): %v", err)
	}
	urlFernet, err := newFernet(urlsafe)
	if err != nil {
		t.Fatalf("newFernet(urlsafe): %v", err)
	}
	if !bytes.Equal(stdFernet.signKey, urlFernet.signKey) || !bytes.Equal(stdFernet.encKey, urlFernet.encKey) {
		t.Fatal("standard and urlsafe keys decoded to different Fernet keys")
	}
}

// 四种编码都解不开时，错误里必须带出第一个非法字符与偏移。
func TestNewFernetReportsInvalidCharacterOffset(t *testing.T) {
	_, err := newFernet("abc*def")
	if err == nil {
		t.Fatal("newFernet accepted an invalid key")
	}
	for _, want := range []string{"'*'", "offset 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}

	// 合法 base64 但长度不对：报出实际字节数。
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err = newFernet(short)
	if err == nil || !strings.Contains(err.Error(), "must decode to 32 bytes") {
		t.Fatalf("short key error = %v", err)
	}

	// 空值（包括成对空引号）必须被拒绝。
	for _, key := range []string{"", `""`, "   "} {
		if _, err := newFernet(key); err == nil {
			t.Errorf("newFernet(%q) succeeded, want error", key)
		}
	}
}

// 复现用户现场：encryption.key 里 OAUTH_ENCRYPTION_KEY 是标准 base64 且带
// 引号，setup-token 必须成功并打印 verified 行。
func TestSetupTokenAcceptsStandardBase64KeyFile(t *testing.T) {
	_, std, _ := standardBase64TestKey(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	csHome := filepath.Join(home, ".claude-science")
	if err := os.MkdirAll(csHome, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "ANTHROPIC_API_KEY_ENCRYPTION_KEY=" + std + "\n" +
		"OAUTH_ENCRYPTION_KEY=\"" + std + "\"\n" +
		"JWT_SIGNING_SECRET=" + std + "\n"
	if err := os.WriteFile(filepath.Join(csHome, "encryption.key"), []byte(content), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	var stdout bytes.Buffer
	if err := runSetupToken(&stdout); err != nil {
		t.Fatalf("runSetupToken with standard base64 key: %v", err)
	}
	if !strings.Contains(stdout.String(), "verified: token decrypts") {
		t.Errorf("stdout missing verification line:\n%s", stdout.String())
	}

	written, err := os.ReadFile(filepath.Join(csHome, ".oauth-tokens", fakeAccountUUID+".enc"))
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	f, err := newFernet(std)
	if err != nil {
		t.Fatalf("newFernet: %v", err)
	}
	if _, err := f.decrypt(string(written)); err != nil {
		t.Fatalf("decrypt written token with standard base64 key: %v", err)
	}
}
