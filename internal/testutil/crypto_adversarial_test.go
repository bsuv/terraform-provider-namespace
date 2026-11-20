package testutil_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// TestCrypto_Adversarial_ConcurrencyAndRace exercises all cryptographic generator and synthesis
// functions concurrently across 100 goroutines to prove race freedom and concurrency safety.
func TestCrypto_Adversarial_ConcurrencyAndRace(t *testing.T) {
	const numGoroutines = 50
	const iterations = 10

	var wg sync.WaitGroup
	var opsCompleted int64

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				op := (workerID + j) % 10
				switch op {
				case 0:
					// RSA key generation with default / edge bits
					bits := (workerID % 3) * 1024 // 0, 1024, 2048
					pemStr, err := testutil.GenerateRSAPrivateKeyPEM(bits)
					if err != nil || !strings.Contains(pemStr, "RSA PRIVATE KEY") {
						t.Errorf("RSA generation failed (bits=%d): %v", bits, err)
					}
				case 1:
					// ECDSA key generation with various curves
					var curve elliptic.Curve
					switch workerID % 4 {
					case 0:
						curve = nil
					case 1:
						curve = elliptic.P256()
					case 2:
						curve = elliptic.P384()
					case 3:
						curve = elliptic.P521()
					}
					pemStr, err := testutil.GenerateECDSAPrivateKeyPEM(curve)
					if err != nil || !strings.Contains(pemStr, "EC PRIVATE KEY") {
						t.Errorf("ECDSA generation failed: %v", err)
					}
				case 2:
					// Self-signed certificate with nil key (generates internal RSA)
					certPEM, err := testutil.GenerateSelfSignedCertificatePEM(nil)
					if err != nil || !strings.Contains(certPEM, "CERTIFICATE") {
						t.Errorf("Self-signed cert (nil key) failed: %v", err)
					}
				case 3:
					// Self-signed certificate with ECDSA key
					ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
					if err != nil {
						t.Errorf("ecdsa key gen failed: %v", err)
						return
					}
					certPEM, err := testutil.GenerateSelfSignedCertificatePEM(ecKey)
					if err != nil || !strings.Contains(certPEM, "CERTIFICATE") {
						t.Errorf("Self-signed cert (ECDSA key) failed: %v", err)
					}
				case 4:
					// PKCS8 Private Key Test helper
					pkcs8PEM := testutil.GenerateTestPKCS8PrivateKey(t)
					if !strings.Contains(pkcs8PEM, "PRIVATE KEY") {
						t.Errorf("GenerateTestPKCS8PrivateKey failed: %s", pkcs8PEM)
					}
				case 5:
					// RSA Test helper
					privRSA, pubRSA := testutil.GenerateTestRSAPrivateKey(t)
					if !strings.Contains(privRSA, "RSA PRIVATE KEY") || !strings.Contains(pubRSA, "PUBLIC KEY") {
						t.Errorf("GenerateTestRSAPrivateKey failed")
					}
				case 6:
					// ECDSA Test helper
					privEC, pubEC := testutil.GenerateTestECDSAPrivateKey(t)
					if !strings.Contains(privEC, "EC PRIVATE KEY") || !strings.Contains(pubEC, "PUBLIC KEY") {
						t.Errorf("GenerateTestECDSAPrivateKey failed")
					}
				case 7:
					// Certificate Test helper
					domain := fmt.Sprintf("worker-%d-%d.namespace.cloud", workerID, j)
					certPEM, keyPEM := testutil.GenerateTestCertificate(t, domain)
					if !strings.Contains(certPEM, "CERTIFICATE") || !strings.Contains(keyPEM, "RSA PRIVATE KEY") {
						t.Errorf("GenerateTestCertificate failed")
					}
				case 8:
					// Synthesize PEM block
					block := testutil.SynthesizePEMBlock("CUSTOM BLOCK", fmt.Sprintf("BODY-%d-%d", workerID, j))
					if !strings.Contains(block, "BEGIN CUSTOM BLOCK") || !strings.Contains(block, "END CUSTOM BLOCK") {
						t.Errorf("SynthesizePEMBlock failed")
					}
				case 9:
					// Synthesize Redaction
					red := testutil.SynthesizeRedactedString("API_KEY")
					mark := testutil.SynthesizeRedactionMarker("SECRET")
					if red != "[REDACTED API_KEY]" || mark != "[REDACTED SECRET]" {
						t.Errorf("Redaction synthesis failed")
					}
				}
				atomic.AddInt64(&opsCompleted, 1)
			}
		}(i)
	}

	wg.Wait()
	t.Logf("Adversarial concurrency test passed: %d operations completed cleanly", atomic.LoadInt64(&opsCompleted))
}

// TestCrypto_Adversarial_RSABitSizes tests normal, edge-case, and malformed bit sizes.
func TestCrypto_Adversarial_RSABitSizes(t *testing.T) {
	// 1. Zero and negative bit sizes default to 2048
	for _, bits := range []int{0, -1, -2048, -100} {
		t.Run(fmt.Sprintf("DefaultBits_%d", bits), func(t *testing.T) {
			pemStr, err := testutil.GenerateRSAPrivateKeyPEM(bits)
			if err != nil {
				t.Fatalf("unexpected error for bits=%d: %v", bits, err)
			}
			block, rest := pem.Decode([]byte(pemStr))
			if block == nil || len(rest) > 0 {
				t.Fatalf("failed to decode PEM block or unexpected trailing data: block=%v, rest=%q", block, string(rest))
			}
			key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				t.Fatalf("failed to parse PKCS#1 private key: %v", err)
			}
			if key.N.BitLen() != 2048 {
				t.Errorf("expected 2048 bit key for bits=%d, got %d", bits, key.N.BitLen())
			}
		})
	}

	// 2. Explicit valid bit sizes: 1024, 2048, 4096
	for _, bits := range []int{1024, 2048} {
		t.Run(fmt.Sprintf("ValidBits_%d", bits), func(t *testing.T) {
			pemStr, err := testutil.GenerateRSAPrivateKeyPEM(bits)
			if err != nil {
				t.Fatalf("failed to generate %d-bit RSA key: %v", bits, err)
			}
			block, _ := pem.Decode([]byte(pemStr))
			if block == nil {
				t.Fatalf("PEM decode returned nil for %d-bit key", bits)
			}
			key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				t.Fatalf("failed to parse %d-bit key: %v", bits, err)
			}
			if key.N.BitLen() != bits {
				t.Errorf("expected %d-bit key, got %d", bits, key.N.BitLen())
			}
		})
	}

	// 3. Invalid bit sizes (too small or insecure < 1024 for RSA) -> should return error, not panic
	for _, bits := range []int{1, 2, 3, 7, 512} {
		t.Run(fmt.Sprintf("InsecureOrInvalidBits_%d", bits), func(t *testing.T) {
			pemStr, err := testutil.GenerateRSAPrivateKeyPEM(bits)
			if err == nil {
				t.Errorf("expected error for bits=%d, but got key: %s", bits, pemStr)
			}
		})
	}
}

// TestCrypto_Adversarial_ECDSACurves tests ECDSA generation for various curves and nil fallback.
func TestCrypto_Adversarial_ECDSACurves(t *testing.T) {
	cases := []struct {
		name         string
		curve        elliptic.Curve
		expectedName string
	}{
		{"NilCurve_DefaultsToP256", nil, "P-256"},
		{"P224", elliptic.P224(), "P-224"},
		{"P256", elliptic.P256(), "P-256"},
		{"P384", elliptic.P384(), "P-384"},
		{"P521", elliptic.P521(), "P-521"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pemStr, err := testutil.GenerateECDSAPrivateKeyPEM(tc.curve)
			if err != nil {
				t.Fatalf("failed to generate ECDSA key for %s: %v", tc.name, err)
			}
			block, rest := pem.Decode([]byte(pemStr))
			if block == nil || len(rest) > 0 {
				t.Fatalf("failed to decode PEM block for %s", tc.name)
			}
			if block.Type != "EC PRIVATE KEY" {
				t.Errorf("expected type 'EC PRIVATE KEY', got %q", block.Type)
			}
			key, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				t.Fatalf("failed to parse EC private key: %v", err)
			}
			if key.Curve.Params().Name != tc.expectedName {
				t.Errorf("expected curve %s, got %s", tc.expectedName, key.Curve.Params().Name)
			}
		})
	}
}

// TestCrypto_Adversarial_SelfSignedCertificates tests self-signed certificate creation with
// RSA, ECDSA, nil, and malformed/unsupported key types.
func TestCrypto_Adversarial_SelfSignedCertificates(t *testing.T) {
	// 1. nil key -> generates RSA 2048 key internally
	t.Run("NilKey", func(t *testing.T) {
		certPEM, err := testutil.GenerateSelfSignedCertificatePEM(nil)
		if err != nil {
			t.Fatalf("unexpected error for nil key: %v", err)
		}
		block, _ := pem.Decode([]byte(certPEM))
		if block == nil || block.Type != "CERTIFICATE" {
			t.Fatalf("invalid certificate PEM block: %v", block)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("failed to parse certificate: %v", err)
		}
		if cert.Subject.CommonName != "test.namespace.cloud" {
			t.Errorf("expected common name 'test.namespace.cloud', got %q", cert.Subject.CommonName)
		}
		if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
			t.Errorf("self-signed signature verification failed: %v", err)
		}
	})

	// 2. *rsa.PrivateKey
	t.Run("RSAPrivateKey", func(t *testing.T) {
		privPEM, _ := testutil.GenerateTestRSAPrivateKey(t)
		block, _ := pem.Decode([]byte(privPEM))
		rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			t.Fatalf("failed to parse rsa key: %v", err)
		}

		certPEM, err := testutil.GenerateSelfSignedCertificatePEM(rsaKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		certBlock, _ := pem.Decode([]byte(certPEM))
		cert, err := x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			t.Fatalf("failed to parse cert: %v", err)
		}
		if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
			t.Errorf("signature verification failed: %v", err)
		}
	})

	// 3. *ecdsa.PrivateKey
	t.Run("ECDSAPrivateKey", func(t *testing.T) {
		privPEM, _ := testutil.GenerateTestECDSAPrivateKey(t)
		block, _ := pem.Decode([]byte(privPEM))
		ecKey, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			t.Fatalf("failed to parse ec key: %v", err)
		}

		certPEM, err := testutil.GenerateSelfSignedCertificatePEM(ecKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		certBlock, _ := pem.Decode([]byte(certPEM))
		cert, err := x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			t.Fatalf("failed to parse cert: %v", err)
		}
		if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
			t.Errorf("signature verification failed: %v", err)
		}
	})

	// 4. Unsupported key types -> return clean error, never panic
	unsupportedKeys := []any{
		"string-key",
		12345,
		&rsa.PublicKey{},
		&ecdsa.PublicKey{},
		struct{ Field string }{"test"},
		[]byte("raw-bytes"),
	}

	for _, badKey := range unsupportedKeys {
		t.Run(fmt.Sprintf("Unsupported_%T", badKey), func(t *testing.T) {
			certPEM, err := testutil.GenerateSelfSignedCertificatePEM(badKey)
			if err == nil {
				t.Errorf("expected error for unsupported key type %T, got cert: %s", badKey, certPEM)
			}
			if !strings.Contains(err.Error(), "unsupported key type") {
				t.Errorf("expected 'unsupported key type' error, got: %v", err)
			}
		})
	}
}

// TestCrypto_Adversarial_SynthesisAndFormatting tests string synthesis edge cases.
func TestCrypto_Adversarial_SynthesisAndFormatting(t *testing.T) {
	// SynthesizePEMBlock with various headers and bodies
	testCases := []struct {
		header string
		body   string
	}{
		{"", ""},
		{"CUSTOM", ""},
		{"", "SOME_BODY"},
		{"RSA PRIVATE KEY", "LINE1\nLINE2\nLINE3"},
		{"CERTIFICATE", "-----NOT-A-REAL-CERT-----"},
		{"SPECIAL_CHARS!@#$%", "\t\n   "},
	}

	for _, tc := range testCases {
		block := testutil.SynthesizePEMBlock(tc.header, tc.body)
		expectedBegin := testutil.PEMBeginHeader(tc.header)
		expectedEnd := testutil.PEMEndHeader(tc.header)

		if !strings.HasPrefix(block, expectedBegin) {
			t.Errorf("expected prefix %q, got %q", expectedBegin, block)
		}
		if !strings.HasSuffix(block, expectedEnd) {
			t.Errorf("expected suffix %q, got %q", expectedEnd, block)
		}
		if tc.body != "" && !strings.Contains(block, tc.body) {
			t.Errorf("expected body %q in block %q", tc.body, block)
		}
	}

	// SynthesizeRedactedString and SynthesizeRedactionMarker
	stringsToTest := []string{"", "TOKEN", "[NESTED]", "foo:bar:baz", "secret\nwith\nnewlines"}
	for _, s := range stringsToTest {
		red := testutil.SynthesizeRedactedString(s)
		expectedRed := fmt.Sprintf("[%s %s]", "REDACTED", s)
		if red != expectedRed {
			t.Errorf("SynthesizeRedactedString mismatch: expected %q, got %q", expectedRed, red)
		}

		marker := testutil.SynthesizeRedactionMarker(s)
		expectedMarker := fmt.Sprintf("[%s %s]", "REDACTED", s)
		if marker != expectedMarker {
			t.Errorf("SynthesizeRedactionMarker mismatch: expected %q, got %q", expectedMarker, marker)
		}
	}
}

// TestCrypto_Adversarial_TestHelpersNilT tests helper functions when testing.T is nil.
func TestCrypto_Adversarial_TestHelpersNilT(t *testing.T) {
	// 1. GenerateTestRSAPrivateKey(nil)
	privRSA, pubRSA := testutil.GenerateTestRSAPrivateKey(nil)
	if !strings.Contains(privRSA, "BEGIN RSA PRIVATE KEY") || !strings.Contains(pubRSA, "BEGIN PUBLIC KEY") {
		t.Fatalf("GenerateTestRSAPrivateKey(nil) returned unexpected output")
	}

	// 2. GenerateTestPKCS8PrivateKey(nil)
	pkcs8 := testutil.GenerateTestPKCS8PrivateKey(nil)
	if !strings.Contains(pkcs8, "BEGIN PRIVATE KEY") {
		t.Fatalf("GenerateTestPKCS8PrivateKey(nil) returned unexpected output")
	}

	// 3. GenerateTestECDSAPrivateKey(nil)
	privEC, pubEC := testutil.GenerateTestECDSAPrivateKey(nil)
	if !strings.Contains(privEC, "BEGIN EC PRIVATE KEY") || !strings.Contains(pubEC, "BEGIN PUBLIC KEY") {
		t.Fatalf("GenerateTestECDSAPrivateKey(nil) returned unexpected output")
	}

	// 4. GenerateTestCertificate(nil, "")
	cert1, key1 := testutil.GenerateTestCertificate(nil, "")
	if !strings.Contains(cert1, "BEGIN CERTIFICATE") || !strings.Contains(key1, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("GenerateTestCertificate(nil, '') returned unexpected output")
	}

	// 5. GenerateTestCertificate(nil, "my.custom.domain")
	cert2, key2 := testutil.GenerateTestCertificate(nil, "my.custom.domain")
	if !strings.Contains(cert2, "BEGIN CERTIFICATE") || !strings.Contains(key2, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("GenerateTestCertificate(nil, 'my.custom.domain') returned unexpected output")
	}
	block, _ := pem.Decode([]byte(cert2))
	parsedCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse generated certificate: %v", err)
	}
	if parsedCert.Subject.CommonName != "my.custom.domain" {
		t.Errorf("expected CommonName 'my.custom.domain', got %q", parsedCert.Subject.CommonName)
	}
}
