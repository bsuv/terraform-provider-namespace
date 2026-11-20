package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/iam/v1beta/iamv1betagrpc"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"namespacelabs.dev/integrations/nsc/grpcapi"
)

type testEchoTokenServer struct {
	iamv1betagrpc.UnimplementedTokenServiceServer
	gotAuth string
}

func (s *testEchoTokenServer) IssueTenantToken(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			s.gotAuth = vals[0]
		}
	}
	return &iamv1beta.IssueTenantTokenResponse{BearerToken: "ok"}, nil
}

func generateLoopbackTestCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	if err != nil {
		t.Fatalf("failed to create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		t.Fatalf("failed to marshal key: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("failed to load X509KeyPair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("failed to append test certificate to root pool")
	}
	return tlsCert, pool
}

func TestNewDefaultTLSConfig_EnforcesTLS12(t *testing.T) {
	cfg := newDefaultTLSConfig()
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("expected MinVersion == tls.VersionTLS12 (%d), got %d", tls.VersionTLS12, cfg.MinVersion)
	}

	// Verify all 3 constructor structs default to TLS transport credentials when Creds is nil
	pool, err := NewEndpointPool(EndpointConfig{
		TokenSource: StaticTokenSource{Token: "test-token"},
	})
	if err != nil {
		t.Fatalf("NewEndpointPool failed: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if pool.creds == nil || pool.creds.Info().SecurityProtocol != "tls" {
		t.Fatalf("expected EndpointPool default creds to use tls, got %v", pool.creds)
	}

	sts := NewSessionTokenSource("st_dummy", "", nil)
	if sts.creds == nil || sts.creds.Info().SecurityProtocol != "tls" {
		t.Fatalf("expected SessionTokenSource default creds to use tls, got %v", sts.creds)
	}

	lts := NewLocalTokenSource("/tmp/does-not-matter.json", "", nil)
	if lts.creds == nil || lts.creds.Info().SecurityProtocol != "tls" {
		t.Fatalf("expected LocalTokenSource default creds to use tls, got %v", lts.creds)
	}

	// Verify TLS handshake rejects TLS 1.1 server and accepts TLS 1.2 server when using newDefaultTLSConfig()
	tlsCert, rootPool := generateLoopbackTestCert(t)

	// Server pinned strictly to TLS 1.1
	ln11, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion:   tls.VersionTLS10,
		MaxVersion:   tls.VersionTLS11,
		Certificates: []tls.Certificate{tlsCert},
	})
	if err != nil {
		t.Fatalf("failed to start TLS 1.1 listener: %v", err)
	}
	defer func() { _ = ln11.Close() }()

	done11 := make(chan struct{})
	go func() {
		defer close(done11)
		conn, acceptErr := ln11.Accept()
		if acceptErr == nil && conn != nil {
			_ = conn.(*tls.Conn).Handshake()
			_ = conn.Close()
		}
	}()

	clientCfg11 := newDefaultTLSConfig()
	clientCfg11.RootCAs = rootPool
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	conn11, dialErr11 := tls.DialWithDialer(dialer, "tcp", ln11.Addr().String(), clientCfg11)
	if dialErr11 == nil {
		_ = conn11.Close()
		t.Fatal("expected TLS 1.2+ client config to reject TLS 1.1 server, but handshake succeeded")
	}
	<-done11

	// Server supporting TLS 1.2
	ln12, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{tlsCert},
	})
	if err != nil {
		t.Fatalf("failed to start TLS 1.2 listener: %v", err)
	}
	defer func() { _ = ln12.Close() }()

	done12 := make(chan struct{})
	go func() {
		defer close(done12)
		conn, acceptErr := ln12.Accept()
		if acceptErr == nil && conn != nil {
			_ = conn.(*tls.Conn).Handshake()
			_ = conn.Close()
		}
	}()

	clientCfg12 := newDefaultTLSConfig()
	clientCfg12.RootCAs = rootPool
	conn12, dialErr12 := tls.DialWithDialer(dialer, "tcp", ln12.Addr().String(), clientCfg12)
	if dialErr12 != nil {
		t.Fatalf("expected TLS 1.2+ client config to accept TLS 1.2 server, got error: %v", dialErr12)
	}
	_ = conn12.Close()
	<-done12
}

func TestTokenCredWrapper_TLSAlwaysRequiresTransportSecurity(t *testing.T) {
	orig := grpcapi.RequireTransportSecurity
	t.Cleanup(func() {
		grpcapi.RequireTransportSecurity = orig
	})

	tlsCreds := credentials.NewTLS(newDefaultTLSConfig())
	insecCreds := insecure.NewCredentials()

	// 1. Even when grpcapi.RequireTransportSecurity is false, TLS creds keep RequireTransportSecurity() == true.
	grpcapi.RequireTransportSecurity = false

	if shouldAllowInsecurePerRPCCreds(nil) {
		t.Fatal("nil creds must not allow insecure per-RPC credentials")
	}
	if shouldAllowInsecurePerRPCCreds(tlsCreds) {
		t.Fatal("TLS creds must not allow insecure per-RPC credentials even when grpcapi.RequireTransportSecurity is false")
	}

	tlsWrapper := tokenCredWrapper{
		token:         StaticTokenSource{Token: "nsct_tls_secret"},
		allowInsecure: shouldAllowInsecurePerRPCCreds(tlsCreds),
	}
	if !tlsWrapper.RequireTransportSecurity() {
		t.Fatal("TLS connection wrapper must return RequireTransportSecurity() == true when grpcapi.RequireTransportSecurity is false")
	}

	// Insecure creds with grpcapi.RequireTransportSecurity == false allow insecure (for bufconn tests).
	insecWrapperAllowed := tokenCredWrapper{
		token:         StaticTokenSource{Token: "nsct_bufconn_secret"},
		allowInsecure: shouldAllowInsecurePerRPCCreds(insecCreds),
	}
	if insecWrapperAllowed.RequireTransportSecurity() {
		t.Fatal("insecure creds with grpcapi.RequireTransportSecurity == false should return RequireTransportSecurity() == false")
	}

	// 2. When grpcapi.RequireTransportSecurity is true, even insecure creds enforce RequireTransportSecurity() == true.
	grpcapi.RequireTransportSecurity = true
	insecWrapperBlocked := tokenCredWrapper{
		token:         StaticTokenSource{Token: "nsct_blocked_secret"},
		allowInsecure: shouldAllowInsecurePerRPCCreds(insecCreds),
	}
	if !insecWrapperBlocked.RequireTransportSecurity() {
		t.Fatal("insecure creds with grpcapi.RequireTransportSecurity == true must require transport security")
	}

	// Verify NewConnectionWithEndpointWithTransportCredentials rejects insecure per-RPC credentials when grpcapi.RequireTransportSecurity == true.
	_, err := NewConnectionWithEndpointWithTransportCredentials(
		context.Background(),
		"https://127.0.0.1:443",
		StaticTokenSource{Token: "nsct_blocked"},
		insecure.NewCredentials(),
	)
	if err == nil || !strings.Contains(err.Error(), "transport level security") {
		t.Fatalf("expected transport level security error when dialing insecure with RequireTransportSecurity=true, got: %v", err)
	}

	// 3. Verify end-to-end TLS gRPC connection succeeds with StaticTokenSource while grpcapi.RequireTransportSecurity == false.
	grpcapi.RequireTransportSecurity = false
	tlsCert, rootPool := generateLoopbackTestCert(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer func() { _ = lis.Close() }()

	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{tlsCert},
	})))
	echoSrv := &testEchoTokenServer{}
	iamv1betagrpc.RegisterTokenServiceServer(srv, echoSrv)
	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)

	clientTLS := newDefaultTLSConfig()
	clientTLS.RootCAs = rootPool
	conn, err := NewConnectionWithEndpointWithTransportCredentials(
		context.Background(),
		"https://"+lis.Addr().String(),
		StaticTokenSource{Token: "nsct_tls_e2e_token"},
		credentials.NewTLS(clientTLS),
	)
	if err != nil {
		t.Fatalf("failed to create TLS client connection: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := iamv1betagrpc.NewTokenServiceClient(conn)
	if _, err := client.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{}); err != nil {
		t.Fatalf("TLS gRPC RPC failed: %v", err)
	}
	if echoSrv.gotAuth != "Bearer nsct_tls_e2e_token" {
		t.Fatalf("expected server to receive Bearer token over TLS, got %q", echoSrv.gotAuth)
	}
}
