package subscription

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// A Tailscale client link carries its auth key encrypted under a key derived
// from the subscription's salt, so a link that outlives its subscription (the
// group deleted or re-salted) stops revealing the auth key. Only the link is
// sealed: the profile served for it embeds the plaintext key, because the
// client has to log in with it. The dashboard encrypts in the browser with
// WebCrypto, so both sides must follow exactly this scheme:
//
//	key   = HKDF-SHA256(IKM = trim(salt), salt = empty, info = authKeyKDFInfo, L = 32)
//	value = "v1." + base64url-nopad(nonce[12] || AES-256-GCM(key, nonce, authKey, AAD = hostname))
//
// The hostname as additional data keeps a value from being replayed under
// another device name. The subscription service never sees a salt: the hub
// publishes each token's derived key in a key file next to (not under) the
// public subscription directory, and deletes it with the subscription.
const (
	authKeyKDFInfo       = "singbox-deploy/tailscale-authkey/v1"
	authKeyValuePrefix   = "v1."
	authKeyCipherKeySize = 32
	authKeyNonceSize     = 12
	authKeyTagSize       = 16
)

// encryptedAuthKeyRE bounds an encrypted value by what a valid auth key can
// encrypt to: "tskey-" plus 1 to 200 characters, with the nonce and tag, is 35
// to 234 bytes, or 47 to 312 base64 characters.
var encryptedAuthKeyRE = regexp.MustCompile(`^v1\.[A-Za-z0-9_-]{47,312}$`)

var (
	// errAuthKeyNotEncrypted answers a link whose authkey is not an encrypted
	// value, a plaintext key included: such a link would give the key away
	// forever, so it is refused rather than served.
	errAuthKeyNotEncrypted = errors.New("authkey must be encrypted; generate the link from the dashboard")
	// errAuthKeyMismatch answers an encrypted value that does not open: sealed
	// for another subscription or hostname, or altered. It deliberately says
	// no more than that.
	errAuthKeyMismatch = errors.New("authkey does not belong to this subscription and hostname; generate the link again from the dashboard")
	errInvalidAuthKey  = errors.New("invalid tailscale authkey")
)

// AuthKeyCipherKey derives the 32-byte AES key from a subscription salt,
// trimmed the same way the URL token's salt is.
func AuthKeyCipherKey(salt string) []byte {
	key, err := hkdf.Key(sha256.New, []byte(strings.TrimSpace(salt)), nil, authKeyKDFInfo, authKeyCipherKeySize)
	if err != nil {
		// Only an output longer than 255 hash blocks can fail.
		panic(err)
	}
	return key
}

// AuthKeyCipherKeyFile is the content of a token's key file: the derived key
// as lowercase hex and a newline.
func AuthKeyCipherKeyFile(salt string) []byte {
	return []byte(hex.EncodeToString(AuthKeyCipherKey(salt)) + "\n")
}

// ParseAuthKeyCipherKeyFile reads a key file written by AuthKeyCipherKeyFile.
func ParseAuthKeyCipherKeyFile(data []byte) ([]byte, error) {
	key, err := hex.DecodeString(string(bytes.TrimSpace(data)))
	if err != nil || len(key) != authKeyCipherKeySize {
		return nil, errors.New("malformed auth key cipher key file")
	}
	return key, nil
}

// EncryptAuthKey seals authKey for a client link with the given hostname
// under a key from AuthKeyCipherKey, with a fresh random nonce.
func EncryptAuthKey(key []byte, hostname, authKey string) (string, error) {
	nonce := make([]byte, authKeyNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return sealAuthKey(key, nonce, hostname, authKey)
}

func sealAuthKey(key, nonce []byte, hostname, authKey string) (string, error) {
	if !tailscaleAuthKeyRE.MatchString(authKey) {
		return "", errInvalidAuthKey
	}
	aead, err := authKeyAEAD(key)
	if err != nil {
		return "", err
	}
	if len(nonce) != authKeyNonceSize {
		return "", fmt.Errorf("auth key nonce must be %d bytes", authKeyNonceSize)
	}
	sealed := aead.Seal(bytes.Clone(nonce), nonce, []byte(authKey), []byte(hostname))
	return authKeyValuePrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// DecryptAuthKey opens a value sealed by EncryptAuthKey for hostname and
// checks that it holds an auth key. Its errors never carry the value or the
// plaintext.
func DecryptAuthKey(key []byte, hostname, value string) (string, error) {
	aead, err := authKeyAEAD(key)
	if err != nil {
		return "", err
	}
	if !encryptedAuthKeyRE.MatchString(value) {
		return "", errAuthKeyNotEncrypted
	}
	sealed, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(value, authKeyValuePrefix))
	if err != nil || len(sealed) < authKeyNonceSize+authKeyTagSize {
		return "", errAuthKeyNotEncrypted
	}
	plain, err := aead.Open(nil, sealed[:authKeyNonceSize], sealed[authKeyNonceSize:], []byte(hostname))
	if err != nil || !tailscaleAuthKeyRE.Match(plain) {
		return "", errAuthKeyMismatch
	}
	return string(plain), nil
}

func authKeyAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != authKeyCipherKeySize {
		return nil, fmt.Errorf("auth key cipher key must be %d bytes", authKeyCipherKeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
