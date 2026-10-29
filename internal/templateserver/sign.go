package templateserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Signer 使用 Ed25519 对 manifest 原始字节签名。
type Signer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// NewSigner 接受 base64 或 hex 编码的 32 字节 seed / 64 字节私钥。
func NewSigner(encoded string) (*Signer, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("signing key is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = hex.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("signing key must be base64 or hex")
		}
	}
	switch len(raw) {
	case ed25519.SeedSize:
		priv := ed25519.NewKeyFromSeed(raw)
		return &Signer{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
	case ed25519.PrivateKeySize:
		priv := ed25519.PrivateKey(append([]byte(nil), raw...))
		return &Signer{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
	default:
		return nil, fmt.Errorf("signing key must be 32 or 64 bytes, got %d", len(raw))
	}
}

// GenerateSigner 仅用于本地开发；每次重启都会换钥。
func GenerateSigner() (*Signer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Signer{priv: priv, pub: pub}, nil
}

func (s *Signer) PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(s.pub)
}

func (s *Signer) Sign(message []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, message))
}

// Verify 供测试与发布自检使用。
func Verify(publicKeyBase64 string, message []byte, signatureBase64 string) bool {
	pub, err := base64.StdEncoding.DecodeString(publicKeyBase64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(signatureBase64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), message, sig)
}
