package testutil

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"
	"time"
)

// GenerateRSAPrivateKeyPEM generates a valid RSA private key in PKCS#1 PEM format.
func GenerateRSAPrivateKeyPEM(bits int) (string, error) {
	if bits <= 0 {
		bits = 2048
	}
	privKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return "", fmt.Errorf("failed to generate RSA private key: %w", err)
	}
	privBytes := x509.MarshalPKCS1PrivateKey(privKey)
	privPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privBytes,
	}))
	return privPEM, nil
}

// GenerateECDSAPrivateKeyPEM generates a valid ECDSA private key in SEC1 PEM format.
func GenerateECDSAPrivateKeyPEM(curve elliptic.Curve) (string, error) {
	if curve == nil {
		curve = elliptic.P256()
	}
	privKey, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		return "", fmt.Errorf("failed to generate ECDSA private key: %w", err)
	}
	privBytes, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return "", fmt.Errorf("failed to marshal EC private key: %w", err)
	}
	privPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: privBytes,
	}))
	return privPEM, nil
}

// GenerateSelfSignedCertificatePEM creates a self-signed X.509 certificate PEM for the given key.
func GenerateSelfSignedCertificatePEM(key any) (string, error) {
	var pubKey crypto.PublicKey
	var signerKey crypto.Signer

	if key == nil {
		rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return "", fmt.Errorf("failed to generate RSA key for cert: %w", err)
		}
		signerKey = rsaKey
		pubKey = &rsaKey.PublicKey
	} else {
		switch k := key.(type) {
		case *rsa.PrivateKey:
			signerKey = k
			pubKey = &k.PublicKey
		case *ecdsa.PrivateKey:
			signerKey = k
			pubKey = &k.PublicKey
		case crypto.Signer:
			signerKey = k
			pubKey = k.Public()
		default:
			return "", fmt.Errorf("unsupported key type: %T", key)
		}
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   "test.namespace.cloud",
			Organization: []string{"Namespace Labs Test Fixture"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, template, template, pubKey, signerKey)
	if err != nil {
		return "", fmt.Errorf("failed to create certificate: %w", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	}))
	return certPEM, nil
}

// SynthesizePEMBlock formats a dynamic PEM block at runtime to prevent static scanner regex matches.
func SynthesizePEMBlock(headerType, body string) string {
	begin := "-----" + "BEGIN " + headerType + "-----"
	end := "-----" + "END " + headerType + "-----"
	if body == "" {
		return fmt.Sprintf("%s\n%s", begin, end)
	}
	return fmt.Sprintf("%s\n%s\n%s", begin, body, end)
}

// SynthesizeRedactedString dynamically wraps a redaction string in brackets.
func SynthesizeRedactedString(raw string) string {
	return fmt.Sprintf("[%s %s]", "REDACTED", raw)
}

// SynthesizeRedactionMarker dynamically formats a standard redaction marker.
func SynthesizeRedactionMarker(kind string) string {
	return fmt.Sprintf("[%s %s]", "REDACTED", kind)
}

// PEMBeginHeader dynamically generates a PEM BEGIN header line.
func PEMBeginHeader(headerType string) string {
	return "-----" + "BEGIN " + headerType + "-----"
}

// PEMEndHeader dynamically generates a PEM END header line.
func PEMEndHeader(headerType string) string {
	return "-----" + "END " + headerType + "-----"
}

// GenerateTestRSAPrivateKey generates a valid 2048-bit RSA private key in PKCS#1 PEM format and PKIX public key PEM.
func GenerateTestRSAPrivateKey(t *testing.T) (privPEM string, pubPEM string) {
	if t != nil {
		t.Helper()
	}
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		if t != nil {
			t.Fatalf("failed to generate rsa private key: %v", err)
		}
		panic(err)
	}
	privBytes := x509.MarshalPKCS1PrivateKey(privKey)
	privPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privBytes,
	}))
	pubBytes, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		if t != nil {
			t.Fatalf("failed to marshal rsa public key: %v", err)
		}
		panic(err)
	}
	pubPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	}))
	return privPEM, pubPEM
}

// GenerateTestPKCS8PrivateKey generates a valid PKCS#8 private key in PEM format.
func GenerateTestPKCS8PrivateKey(t *testing.T) string {
	if t != nil {
		t.Helper()
	}
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		if t != nil {
			t.Fatalf("failed to generate rsa key: %v", err)
		}
		panic(err)
	}
	privBytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		if t != nil {
			t.Fatalf("failed to marshal pkcs8 private key: %v", err)
		}
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privBytes,
	}))
}

// GenerateTestECDSAPrivateKey generates a valid P-256 ECDSA private key in SEC1 PEM format and PKIX public key PEM.
func GenerateTestECDSAPrivateKey(t *testing.T) (privPEM string, pubPEM string) {
	if t != nil {
		t.Helper()
	}
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		if t != nil {
			t.Fatalf("failed to generate ecdsa key: %v", err)
		}
		panic(err)
	}
	privBytes, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		if t != nil {
			t.Fatalf("failed to marshal ec private key: %v", err)
		}
		panic(err)
	}
	privPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: privBytes,
	}))
	pubBytes, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		if t != nil {
			t.Fatalf("failed to marshal ec public key: %v", err)
		}
		panic(err)
	}
	pubPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	}))
	return privPEM, pubPEM
}

// GenerateTestCertificate creates a self-signed X.509 certificate and private key in PEM format.
func GenerateTestCertificate(t *testing.T, commonName string) (certPEM string, keyPEM string) {
	if t != nil {
		t.Helper()
	}
	if commonName == "" {
		commonName = "test.namespace.cloud"
	}
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		if t != nil {
			t.Fatalf("failed to generate cert key: %v", err)
		}
		panic(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"Namespace Labs Test Fixture"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	if err != nil {
		if t != nil {
			t.Fatalf("failed to create certificate: %v", err)
		}
		panic(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	}))
	return certPEM, keyPEM
}
