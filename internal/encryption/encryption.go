// Package encryption implements the self-contained modalfs v1 wire format.
// See docs/design/encryption.md for the complete format and its limitations.
package encryption

import (
	"bytes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	BlockSize    = 8 << 20
	Overhead     = 48
	ChunkSize    = BlockSize - Overhead
	NamePrefix   = "mfs1_"
	MaxNameBytes = 147
)

var header = []byte{'M', 'F', 'S', '1', 0, 0, 0, 0}
var keyEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

type Key [32]byte

func NewKey() Key {
	var k Key
	rand.Read(k[:])
	return k
}

func (k Key) Text() string {
	h := sha256.Sum256(append([]byte("modalfs/key/v1\x00"), k[:]...))
	data := append(append([]byte{}, k[:]...), h[:4]...)
	s := keyEncoding.EncodeToString(data)
	var groups []string
	for len(s) > 0 {
		n := min(4, len(s))
		groups = append(groups, s[:n])
		s = s[n:]
	}
	return "mfs1-" + strings.Join(groups, "-")
}

func ParseKey(text string) (Key, error) {
	var k Key
	s := strings.ToUpper(strings.TrimSpace(text))
	if !strings.HasPrefix(s, "MFS1-") {
		return k, errors.New("key checksum failed: expected an mfs1 key; paste the complete key")
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '-', ' ', '\t', '\r', '\n':
			return -1
		case 'O':
			return '0'
		case 'I', 'L':
			return '1'
		}
		return r
	}, s[5:])
	b, err := keyEncoding.DecodeString(s)
	if err != nil || len(b) != 36 || keyEncoding.EncodeToString(b) != s {
		return k, errors.New("key checksum failed: paste the complete key without changing any characters")
	}
	copy(k[:], b[:32])
	h := sha256.Sum256(append([]byte("modalfs/key/v1\x00"), k[:]...))
	if !hmac.Equal(b[32:], h[:4]) {
		return Key{}, errors.New("key checksum failed: paste the complete key without changing any characters")
	}
	return k, nil
}

func (k Key) Fingerprint() string {
	h := sha256.Sum256(append([]byte("modalfs/fingerprint/v1\x00"), k[:]...))
	return hex.EncodeToString(h[:16])
}

type purpose struct {
	aead     cipher.AEAD
	nonceKey []byte
}

type Cipher struct {
	key             Key
	contents, names purpose
}

func New(k Key) *Cipher {
	derive := func(label string) []byte {
		b, err := hkdf.Key(sha256.New, k[:], nil, "modalfs/v1/"+label, 32)
		if err != nil {
			panic(err) // fixed HKDF length is always valid
		}
		return b
	}
	makePurpose := func(label string) purpose {
		aead, err := chacha20poly1305.NewX(derive(label + "/key"))
		if err != nil {
			panic(err) // HKDF always returns a 32-byte key
		}
		return purpose{aead, derive(label + "/nonce")}
	}
	return &Cipher{key: k, contents: makePurpose("contents"), names: makePurpose("names")}
}

func (c *Cipher) Key() Key { return c.key }

func (p purpose) nonce(plain, ad []byte) []byte {
	h := hmac.New(sha256.New, p.nonceKey)
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(ad)))
	h.Write(length[:])
	h.Write(ad)
	h.Write(plain)
	return h.Sum(nil)[:24]
}

func (p purpose) seal(plain, ad []byte) []byte {
	nonce := p.nonce(plain, ad)
	return p.aead.Seal(nonce, nonce, plain, ad)
}

func (p purpose) open(data, ad []byte) ([]byte, error) {
	if len(data) < 40 {
		return nil, errors.New("encryption authentication failed: truncated ciphertext")
	}
	plain, err := p.aead.Open(nil, data[:24], data[24:], ad)
	if err != nil || !hmac.Equal(data[:24], p.nonce(plain, ad)) {
		return nil, errors.New("encryption authentication failed: wrong key or damaged data")
	}
	return plain, nil
}

func (c *Cipher) SealBlock(plain []byte) []byte {
	return append(bytes.Clone(header), c.contents.seal(plain, header)...)
}

func (c *Cipher) OpenBlock(data []byte) ([]byte, error) {
	if len(data) < Overhead || !bytes.Equal(data[:8], header) {
		return nil, errors.New("encryption authentication failed: damaged or unsupported block header")
	}
	return c.contents.open(data[8:], header)
}

func (c *Cipher) EncodePath(path string) (string, error) {
	if path == "" || path == "/" {
		return "", nil
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	out := make([]string, len(parts))
	for i, part := range parts {
		if i == 0 && part == ".modalfs" {
			out[i] = part
			continue
		}
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, 0) {
			return "", errors.New("invalid path component")
		}
		if len(part) > MaxNameBytes {
			return "", fmt.Errorf("encrypted names can be at most %d bytes; shorten %q", MaxNameBytes, part)
		}
		ad := []byte("modalfs/v1/name\x00" + strings.Join(parts[:i], "/"))
		out[i] = NamePrefix + base64.RawURLEncoding.EncodeToString(c.names.seal([]byte(part), ad))
	}
	return strings.Join(out, "/"), nil
}

func (c *Cipher) DecodePath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if i == 0 && part == ".modalfs" {
			continue
		}
		if !strings.HasPrefix(part, NamePrefix) {
			return "", fmt.Errorf("unencrypted or unsupported name in encrypted volume: %q", part)
		}
		b, err := base64.RawURLEncoding.Strict().DecodeString(part[len(NamePrefix):])
		if err != nil {
			return "", errors.New("encryption authentication failed: damaged filename encoding")
		}
		ad := []byte("modalfs/v1/name\x00" + strings.Join(parts[:i], "/"))
		plain, err := c.names.open(b, ad)
		if err != nil {
			return "", err
		}
		s := string(plain)
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\x00") || len(s) > MaxNameBytes {
			return "", errors.New("invalid decrypted path component")
		}
		parts[i] = s
	}
	return strings.Join(parts, "/"), nil
}

func CiphertextSize(size int64) int64 {
	return size + max(1, (size+ChunkSize-1)/ChunkSize)*Overhead
}

func PlaintextSize(size int64) (int64, error) {
	if size < Overhead {
		return 0, errors.New("encrypted file is truncated: missing authenticated block")
	}
	n := (size + BlockSize - 1) / BlockSize
	last := size - (n-1)*BlockSize
	if last < Overhead || (last == Overhead && n > 1) {
		return 0, errors.New("encrypted file is truncated: invalid final block size")
	}
	return size - n*Overhead, nil
}
