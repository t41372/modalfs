//go:build e2e && darwin

package e2e

import (
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	endian "encoding/binary"
	"strings"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// This decoder follows docs/design/encryption.md. It deliberately imports
// neither production format package: the key and raw tree must suffice.
func referenceKey(t *testing.T, text string) []byte {
	t.Helper()
	encoding := base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)
	b, err := encoding.DecodeString(strings.ReplaceAll(text[5:], "-", ""))
	if err != nil || len(b) != 36 {
		t.Fatalf("reference key parser: %v", err)
	}
	sum := sha256.Sum256(append([]byte("modalfs/key/v1\x00"), b[:32]...))
	if !bytes.Equal(sum[:4], b[32:]) {
		t.Fatal("reference key checksum mismatch")
	}
	return b[:32]
}

func referenceOpen(t *testing.T, key []byte, purpose string, ad, envelope []byte) []byte {
	t.Helper()
	derive := func(suffix string) []byte {
		b, err := hkdf.Key(sha256.New, key, nil, "modalfs/v1/"+purpose+"/"+suffix, 32)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if len(envelope) < 40 {
		t.Fatal("reference decoder: truncated envelope")
	}
	aead, err := chacha20poly1305.NewX(derive("key"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := aead.Open(nil, envelope[:24], envelope[24:], ad)
	if err != nil {
		t.Fatalf("reference decoder authentication: %v", err)
	}
	mac := hmac.New(sha256.New, derive("nonce"))
	var length [8]byte
	endian.BigEndian.PutUint64(length[:], uint64(len(ad)))
	mac.Write(length[:])
	mac.Write(ad)
	mac.Write(plain)
	if !hmac.Equal(mac.Sum(nil)[:24], envelope[:24]) {
		t.Fatal("reference decoder: noncanonical nonce")
	}
	return plain
}

func referenceName(t *testing.T, key []byte, raw string) string {
	t.Helper()
	parts := strings.Split(raw, "/")
	for i, part := range parts {
		if i == 0 && part == ".modalfs" {
			continue
		}
		if !strings.HasPrefix(part, "mfs1_") {
			t.Fatalf("reference decoder: plaintext component %q", part)
		}
		b, err := base64.RawURLEncoding.Strict().DecodeString(part[5:])
		if err != nil {
			t.Fatal(err)
		}
		ad := []byte("modalfs/v1/name\x00" + strings.Join(parts[:i], "/"))
		parts[i] = string(referenceOpen(t, key, "names", ad, b))
	}
	return strings.Join(parts, "/")
}

func referenceContents(t *testing.T, key, raw []byte) []byte {
	t.Helper()
	if len(raw) < 48 {
		t.Fatal("reference decoder: missing empty-file envelope")
	}
	var out []byte
	for len(raw) > 0 {
		n := min(8388608, len(raw))
		block := raw[:n]
		if len(block) < 48 || !bytes.Equal(block[:8], []byte{'M', 'F', 'S', '1', 0, 0, 0, 0}) {
			t.Fatal("reference decoder: invalid block header")
		}
		out = append(out, referenceOpen(t, key, "contents", block[:8], block[8:])...)
		raw = raw[n:]
	}
	return out
}
