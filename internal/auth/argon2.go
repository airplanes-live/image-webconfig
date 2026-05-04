// Package auth implements webconfig's password hashing, session management,
// login throttling, and password store. Hashing uses argon2id with the PHC
// string format; sessions are in-memory only (service restart = logout).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params for argon2id hashing. All fields validated by Validate() before use;
// defaults match OWASP's 2024 recommendation. Threads must fit in uint8 (the
// argon2 library's parallelism arg is uint8).
type Params struct {
	TimeCost uint32 // iterations; min 1
	MemoryKB uint32 // KB; min 8 (8 KiB)
	Threads  uint8  // parallelism; min 1
	KeyLen   uint32 // hash length in bytes; min 16
	SaltLen  uint32 // salt length in bytes; min 8
}

// DefaultParams is the recommended baseline (OWASP 2024).
var DefaultParams = Params{
	TimeCost: 2,
	MemoryKB: 64 * 1024, // 64 MiB
	Threads:  2,
	KeyLen:   32,
	SaltLen:  16,
}

func (p Params) Validate() error {
	switch {
	case p.TimeCost < 1:
		return errors.New("argon2 time-cost must be >= 1")
	case p.MemoryKB < 8:
		return errors.New("argon2 memory must be >= 8 KiB")
	case p.Threads < 1:
		return errors.New("argon2 threads must be >= 1")
	case p.KeyLen < 16:
		return errors.New("argon2 key length must be >= 16")
	case p.SaltLen < 8:
		return errors.New("argon2 salt length must be >= 8")
	}
	return nil
}

// Hash computes an argon2id PHC-formatted string for the given password using
// a freshly generated cryptorandom salt.
func Hash(password string, p Params) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("argon2 salt: %w", err)
	}
	digest := argon2.IDKey([]byte(password), salt, p.TimeCost, p.MemoryKB, p.Threads, p.KeyLen)
	return formatPHC(p, salt, digest), nil
}

// Verify reports whether password matches the given PHC string. The PHC
// parameters are re-derived from the stored hash, so a future param upgrade
// can transparently re-verify older hashes (with a re-hash on next change).
func Verify(password, phc string) (bool, error) {
	p, salt, want, err := parsePHC(phc)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.TimeCost, p.MemoryKB, p.Threads, p.KeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func formatPHC(p Params, salt, hash []byte) string {
	enc := base64.RawStdEncoding
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		p.MemoryKB, p.TimeCost, p.Threads,
		enc.EncodeToString(salt),
		enc.EncodeToString(hash),
	)
}

// parsePHC accepts only $argon2id$v=19$m=...,t=...,p=...$<salt>$<hash>$ —
// strict format, no extra fields, raw-standard base64 (no padding), each
// parameter present exactly once. Anything else is rejected.
func parsePHC(s string) (Params, []byte, []byte, error) {
	parts := strings.Split(s, "$")
	// Expected: ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 {
		return Params{}, nil, nil, errors.New("phc: wrong field count")
	}
	if parts[0] != "" {
		return Params{}, nil, nil, errors.New("phc: missing leading $")
	}
	if parts[1] != "argon2id" {
		return Params{}, nil, nil, fmt.Errorf("phc: unsupported algorithm %q", parts[1])
	}
	if parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return Params{}, nil, nil, fmt.Errorf("phc: unsupported version %q", parts[2])
	}

	memKB, timeCost, threads, err := parsePHCParams(parts[3])
	if err != nil {
		return Params{}, nil, nil, err
	}

	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("phc: salt decode: %w", err)
	}
	hash, err := enc.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("phc: hash decode: %w", err)
	}
	if len(salt) < 8 {
		return Params{}, nil, nil, errors.New("phc: salt too short")
	}
	if len(hash) < 16 {
		return Params{}, nil, nil, errors.New("phc: hash too short")
	}

	p := Params{
		TimeCost: timeCost,
		MemoryKB: memKB,
		Threads:  threads,
		// uint32 max is fine for a 4-byte length; cast safely.
		KeyLen:  uint32(len(hash)),
		SaltLen: uint32(len(salt)),
	}
	if err := p.Validate(); err != nil {
		return Params{}, nil, nil, fmt.Errorf("phc: %w", err)
	}
	return p, salt, hash, nil
}

func parsePHCParams(s string) (memKB, timeCost uint32, threads uint8, err error) {
	pairs := strings.Split(s, ",")
	if len(pairs) != 3 {
		return 0, 0, 0, errors.New("phc: params must have exactly m, t, p")
	}
	var seenM, seenT, seenP bool
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return 0, 0, 0, fmt.Errorf("phc: malformed param %q", kv)
		}
		switch k {
		case "m":
			if seenM {
				return 0, 0, 0, errors.New("phc: duplicate m")
			}
			seenM = true
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("phc: m: %w", err)
			}
			memKB = uint32(n)
		case "t":
			if seenT {
				return 0, 0, 0, errors.New("phc: duplicate t")
			}
			seenT = true
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("phc: t: %w", err)
			}
			timeCost = uint32(n)
		case "p":
			if seenP {
				return 0, 0, 0, errors.New("phc: duplicate p")
			}
			seenP = true
			n, err := strconv.ParseUint(v, 10, 8)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("phc: p: %w", err)
			}
			threads = uint8(n)
		default:
			return 0, 0, 0, fmt.Errorf("phc: unknown param %q", k)
		}
	}
	if !seenM || !seenT || !seenP {
		return 0, 0, 0, errors.New("phc: missing required params")
	}
	return memKB, timeCost, threads, nil
}
