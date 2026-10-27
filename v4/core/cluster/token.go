package cluster

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/errs"
	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

// NewID returns a prefixed random identifier.
func NewID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return prefix + hex.EncodeToString(b)
}

// NewToken returns a 256-bit URL-safe token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot generate cluster token")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidateToken enforces the long-lived token format. Tokens never expire;
// rotating or reinstalling is the only way to invalidate one.
func ValidateToken(tok string) error {
	if len(tok) < 32 {
		return errs.New(errs.Auth, "cluster token is too short (%d characters)", len(tok)).
			WithHint("use at least 32 characters; 43 URL-safe base64 characters is the default")
	}
	if len(tok) > 128 {
		return errs.New(errs.Auth, "cluster token is too long (%d characters)", len(tok))
	}
	for _, r := range tok {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return errs.New(errs.Auth, "cluster token contains an unsupported character %q", r).
				WithHint("use only A-Z a-z 0-9 - _ (URL-safe base64)")
		}
	}
	return nil
}

// TokenEqual compares tokens in constant time.
func TokenEqual(a, b string) bool {
	if len(a) != len(b) {
		// Still compare to keep timing independent of the first mismatch.
		dummy := make([]byte, len(a))
		subtle.ConstantTimeCompare([]byte(a), dummy)
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// JoinCode is the portable text a new node pastes into `maio cluster join`.
type JoinCode struct {
	ClusterID string `json:"c"`
	Name      string `json:"n"`
	Host      string `json:"h"`
	Port      int    `json:"p"`
	Token     string `json:"t"`
	Protocol  int    `json:"v"`
	// Fingerprint pins an optional TLS certificate (SHA-256 hex).
	Fingerprint string `json:"f,omitempty"`
}

const joinPrefix = "MAIO1-"

// EncodeJoinCode builds the pairing string.
func EncodeJoinCode(c JoinCode) (string, error) {
	if c.Host == "" || c.Port < 1 || c.Port > 65535 || c.Token == "" {
		return "", errs.New(errs.Usage, "join code requires host, port and token")
	}
	if c.Protocol == 0 {
		c.Protocol = version.Protocol
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", errs.Wrap(errs.Internal, err, "cannot encode join code")
	}
	return joinPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeJoinCode parses either a MAIO1- code or a maio:// URL.
func DecodeJoinCode(s string) (JoinCode, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return JoinCode{}, errs.New(errs.Usage, "join code is empty")
	}
	var raw []byte
	switch {
	case strings.HasPrefix(s, joinPrefix):
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, joinPrefix))
		if err != nil {
			return JoinCode{}, errs.New(errs.Usage, "join code is corrupt (base64)").
				WithHint("copy the whole code printed by \"maio cluster join-code\"")
		}
		raw = b
	case strings.HasPrefix(s, "maio://"):
		u, err := url.Parse(s)
		if err != nil {
			return JoinCode{}, errs.Wrap(errs.Usage, err, "invalid maio join URL")
		}
		q := u.Query()
		port, _ := strconv.Atoi(q.Get("port"))
		c := JoinCode{
			ClusterID: q.Get("cluster"), Name: q.Get("name"),
			Host: u.Hostname(), Port: port, Token: q.Get("token"),
			Fingerprint: q.Get("fp"),
		}
		if c.Protocol == 0 {
			c.Protocol = version.Protocol
		}
		return c, validateJoin(c)
	default:
		return JoinCode{}, errs.New(errs.Usage, "unrecognized join code").
			WithHint("expected MAIO1-... or maio://...")
	}
	var c JoinCode
	if err := json.Unmarshal(raw, &c); err != nil {
		return JoinCode{}, errs.Wrap(errs.Usage, err, "join code is corrupt (json)")
	}
	if c.Protocol == 0 {
		c.Protocol = version.Protocol
	}
	return c, validateJoin(c)
}

func validateJoin(c JoinCode) error {
	if c.Host == "" {
		return errs.New(errs.Usage, "join code has no coordinator host")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errs.New(errs.Usage, "join code has an invalid port %d", c.Port)
	}
	if c.Token == "" {
		return errs.New(errs.Usage, "join code has no token")
	}
	if c.Protocol != version.Protocol {
		return errs.New(errs.Unavailable, "join code uses protocol %d, this build speaks %d", c.Protocol, version.Protocol).
			WithHint("install the same maio v4 build on every node")
	}
	return nil
}

// Fingerprint returns the SHA-256 of a certificate DER blob as lowercase hex.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// AdminURL is a convenience for diagnostics.
func (c JoinCode) AdminURL() string {
	return fmt.Sprintf("http://%s", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
}
