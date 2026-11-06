// Command mintjwt creates a throwaway ES256 key, writes its JWKS, and prints a
// signed test token with the given subject and organization claim.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
	"time"
)

const kid = "fixture-key"

func main() {
	keyPath := flag.String("key", "key.pem", "private key, created when missing")
	jwksPath := flag.String("jwks", "jwks.json", "JWKS output")
	sub := flag.String("sub", "user_fixture_1", "sub claim")
	org := flag.String("org", "org_fixture_1", "org_id claim")
	email := flag.String("email", "fixture@example.com", "email claim")
	flag.Parse()

	key := loadOrCreate(*keyPath)
	writeJWKS(*jwksPath, &key.PublicKey)
	claims := map[string]any{
		"iss": "fixture.example.com", "aud": "mcp.example.com", "sub": *sub,
		"org_id": *org, "email": *email,
		"iat": time.Now().Unix(), "exp": time.Now().Add(24 * time.Hour).Unix(),
	}
	fmt.Println(sign(key, claims))
}

func loadOrCreate(path string) *ecdsa.PrivateKey {
	if b, err := os.ReadFile(path); err == nil {
		blk, _ := pem.Decode(b)
		k, err := x509.ParseECPrivateKey(blk.Bytes)
		if err != nil {
			log.Fatal(err)
		}
		return k
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	der, _ := x509.MarshalECPrivateKey(k)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		log.Fatal(err)
	}
	return k
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func writeJWKS(path string, pub *ecdsa.PublicKey) {
	jwks := map[string]any{"keys": []any{map[string]any{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid,
		"x": b64(pub.X.FillBytes(make([]byte, 32))), "y": b64(pub.Y.FillBytes(make([]byte, 32))),
	}}}
	b, _ := json.MarshalIndent(jwks, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		log.Fatal(err)
	}
}

func sign(key *ecdsa.PrivateKey, claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": kid})
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		log.Fatal(err)
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return input + "." + b64(sig)
}
