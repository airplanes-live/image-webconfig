package auth

import (
	"strings"
	"testing"
)

// fastParams keeps tests fast — argon2id with full defaults is ~50ms/hash.
var fastParams = Params{TimeCost: 1, MemoryKB: 8, Threads: 1, KeyLen: 32, SaltLen: 16}

func TestHashAndVerify_RoundTrip(t *testing.T) {
	t.Parallel()
	phc, err := Hash("correct horse battery staple", fastParams)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify("correct horse battery staple", phc)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Verify with correct password returned false")
	}
}

func TestVerify_WrongPassword(t *testing.T) {
	t.Parallel()
	phc, err := Hash("correct", fastParams)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify("wrong", phc)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Verify with wrong password returned true")
	}
}

func TestHash_DifferentSaltsProduceDifferentHashes(t *testing.T) {
	t.Parallel()
	a, _ := Hash("same", fastParams)
	b, _ := Hash("same", fastParams)
	if a == b {
		t.Fatal("two hashes of the same password collided — salt not random")
	}
}

func TestHash_PHCFormatShape(t *testing.T) {
	t.Parallel()
	phc, err := Hash("pw", fastParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$") {
		t.Errorf("PHC missing $argon2id$v=19$ prefix: %q", phc)
	}
	if strings.Count(phc, "$") != 5 {
		t.Errorf("PHC should have 5 $ separators, got %d in %q", strings.Count(phc, "$"), phc)
	}
}

func TestParams_ValidateRejectsBadValues(t *testing.T) {
	t.Parallel()
	cases := map[string]Params{
		"zero time":       {TimeCost: 0, MemoryKB: 8, Threads: 1, KeyLen: 32, SaltLen: 16},
		"sub-minimum mem": {TimeCost: 1, MemoryKB: 4, Threads: 1, KeyLen: 32, SaltLen: 16},
		"zero threads":    {TimeCost: 1, MemoryKB: 8, Threads: 0, KeyLen: 32, SaltLen: 16},
		"short keylen":    {TimeCost: 1, MemoryKB: 8, Threads: 1, KeyLen: 8, SaltLen: 16},
		"short saltlen":   {TimeCost: 1, MemoryKB: 8, Threads: 1, KeyLen: 32, SaltLen: 4},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if err := p.Validate(); err == nil {
				t.Error("Validate accepted bad params")
			}
		})
	}
}

func TestHash_RejectsBadParams(t *testing.T) {
	t.Parallel()
	_, err := Hash("pw", Params{TimeCost: 0, MemoryKB: 8, Threads: 1, KeyLen: 32, SaltLen: 16})
	if err == nil {
		t.Fatal("Hash accepted invalid params")
	}
}

func TestVerify_RejectsMalformedPHC(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"not-a-phc",
		"$argon2i$v=19$m=8,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", // wrong algo
		"$argon2id$v=18$m=8,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", // wrong version
		"$argon2id$v=19$m=8,t=1$YWFhYWFhYWFhYWFhYWFhYQ$YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE",     // missing p param
		"$argon2id$v=19$m=8,t=1,p=1,x=2$YWFhYWFhYWFhYWFhYWFhYQ$YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", // unknown param
		"$argon2id$v=19$m=8,t=1,p=1$!!!notbase64!!!$YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE",      // bad salt b64
		"$argon2id$v=19$m=8,t=1,p=1$YWFhYWFhYWFhYWFhYWFhYQ$short",                                              // short hash
	}
	for _, phc := range cases {
		ok, err := Verify("pw", phc)
		if err == nil {
			t.Errorf("Verify(%q) accepted malformed PHC (ok=%v)", phc, ok)
		}
	}
}

func TestVerify_RejectsDuplicateParams(t *testing.T) {
	t.Parallel()
	// Manually constructed PHC with duplicate t — parser must reject.
	bad := "$argon2id$v=19$m=8,t=1,t=2,p=1$YWFhYWFhYWFhYWFhYWFhYQ$YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	if _, err := Verify("pw", bad); err == nil {
		t.Fatal("duplicate t accepted")
	}
}
