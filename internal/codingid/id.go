package codingid

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

func Token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}

func ID(prefix string) string { return prefix + "_" + Token()[:24] }

func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
