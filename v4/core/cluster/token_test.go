package cluster

import (
	"testing"

	"github.com/ShieldDog-ACE/maio-cluster/v4/core/version"
)

func TestJoinCodeRoundTrip(t *testing.T) {
	in := JoinCode{
		ClusterID: "mc_123", Name: "lab", Host: "192.168.1.5", Port: 47800,
		Token: "0123456789abcdef0123456789abcdef", Protocol: version.Protocol,
	}
	code, err := EncodeJoinCode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeJoinCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if out.ClusterID != in.ClusterID || out.Host != in.Host || out.Port != in.Port || out.Token != in.Token {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	if _, err := DecodeJoinCode(code + "x"); err == nil {
		t.Fatal("corrupt code accepted")
	}
}

func TestJoinURL(t *testing.T) {
	code := "maio://join?cluster=mc_1&name=lab&port=47800&token=abcdef&fp=abc"
	out, err := DecodeJoinCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if out.Host != "join" || out.Port != 47800 || out.ClusterID != "mc_1" {
		t.Fatalf("unexpected: %+v", out)
	}
}

func TestTokenEqual(t *testing.T) {
	if !TokenEqual("abc", "abc") {
		t.Fatal("equal tokens rejected")
	}
	if TokenEqual("abc", "abd") || TokenEqual("abc", "ab") {
		t.Fatal("unequal tokens accepted")
	}
}

func TestValidateToken(t *testing.T) {
	if err := ValidateToken("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"short", "with space 012345678901234567890123456789", "bad+slash/012345678901234567890123"} {
		if err := ValidateToken(bad); err == nil {
			t.Fatalf("accepted invalid token %q", bad)
		}
	}
}
