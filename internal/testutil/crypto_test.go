package testutil_test

import (
	"crypto/elliptic"
	"strings"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func TestCrypto_GenerateRSAPrivateKeyPEM(t *testing.T) {
	pemStr, err := testutil.GenerateRSAPrivateKeyPEM(2048)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(pemStr, "RSA PRIVATE KEY") {
		t.Errorf("expected RSA PRIVATE KEY in output, got:\n%s", pemStr)
	}
}

func TestCrypto_GenerateECDSAPrivateKeyPEM(t *testing.T) {
	pemStr, err := testutil.GenerateECDSAPrivateKeyPEM(elliptic.P256())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(pemStr, "EC PRIVATE KEY") {
		t.Errorf("expected EC PRIVATE KEY in output, got:\n%s", pemStr)
	}
}

func TestCrypto_GenerateSelfSignedCertificatePEM(t *testing.T) {
	certPEM, err := testutil.GenerateSelfSignedCertificatePEM(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(certPEM, "CERTIFICATE") {
		t.Errorf("expected CERTIFICATE in output, got:\n%s", certPEM)
	}
}

func TestCrypto_SynthesizePEMBlock(t *testing.T) {
	block := testutil.SynthesizePEMBlock("TEST KEY", "ABC123XYZ")
	expectedBegin := testutil.PEMBeginHeader("TEST KEY")
	expectedEnd := testutil.PEMEndHeader("TEST KEY")

	if !strings.HasPrefix(block, expectedBegin) {
		t.Errorf("expected block to start with %q, got %q", expectedBegin, block)
	}
	if !strings.HasSuffix(block, expectedEnd) {
		t.Errorf("expected block to end with %q, got %q", expectedEnd, block)
	}
	if !strings.Contains(block, "ABC123XYZ") {
		t.Errorf("expected body in block, got %q", block)
	}

	emptyBlock := testutil.SynthesizePEMBlock("EMPTY", "")
	if !strings.Contains(emptyBlock, testutil.PEMBeginHeader("EMPTY")) || !strings.Contains(emptyBlock, testutil.PEMEndHeader("EMPTY")) {
		t.Errorf("expected headers in empty block, got %q", emptyBlock)
	}
}

func TestCrypto_SynthesizeRedactedStringAndMarkers(t *testing.T) {
	redacted := testutil.SynthesizeRedactedString("FOO")
	if redacted != "[REDACTED FOO]" {
		t.Errorf("expected '[REDACTED FOO]', got %q", redacted)
	}

	marker := testutil.SynthesizeRedactionMarker("PRIVATE KEY")
	if marker != "[REDACTED PRIVATE KEY]" {
		t.Errorf("expected '[REDACTED PRIVATE KEY]', got %q", marker)
	}
}

func TestCrypto_GenerateTestHelpers(t *testing.T) {
	privRSA, pubRSA := testutil.GenerateTestRSAPrivateKey(t)
	if !strings.Contains(privRSA, "RSA PRIVATE KEY") || !strings.Contains(pubRSA, "PUBLIC KEY") {
		t.Errorf("unexpected RSA key pair output")
	}

	pkcs8 := testutil.GenerateTestPKCS8PrivateKey(t)
	if !strings.Contains(pkcs8, "PRIVATE KEY") {
		t.Errorf("unexpected PKCS8 output")
	}

	privEC, pubEC := testutil.GenerateTestECDSAPrivateKey(t)
	if !strings.Contains(privEC, "EC PRIVATE KEY") || !strings.Contains(pubEC, "PUBLIC KEY") {
		t.Errorf("unexpected ECDSA key pair output")
	}

	cert, key := testutil.GenerateTestCertificate(t, "custom.test.domain")
	if !strings.Contains(cert, "CERTIFICATE") || !strings.Contains(key, "RSA PRIVATE KEY") {
		t.Errorf("unexpected certificate/key output")
	}
}
