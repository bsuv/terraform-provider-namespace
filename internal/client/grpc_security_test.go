package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/iam/v1beta/iamv1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/private/sessions/sessionsv1betagrpc"
	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	sessions "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/sessions"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
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
	orig := RequireTransportSecurity
	t.Cleanup(func() {
		RequireTransportSecurity = orig
	})

	tlsCreds := credentials.NewTLS(newDefaultTLSConfig())
	insecCreds := insecure.NewCredentials()

	// 1. Even when RequireTransportSecurity is false, TLS creds keep RequireTransportSecurity() == true.
	RequireTransportSecurity = false

	if shouldAllowInsecurePerRPCCreds(nil) {
		t.Fatal("nil creds must not allow insecure per-RPC credentials")
	}
	if shouldAllowInsecurePerRPCCreds(tlsCreds) {
		t.Fatal("TLS creds must not allow insecure per-RPC credentials even when RequireTransportSecurity is false")
	}

	tlsWrapper := tokenCredWrapper{
		token:         StaticTokenSource{Token: "nsct_tls_secret"},
		allowInsecure: shouldAllowInsecurePerRPCCreds(tlsCreds),
	}
	if !tlsWrapper.RequireTransportSecurity() {
		t.Fatal("TLS connection wrapper must return RequireTransportSecurity() == true when RequireTransportSecurity is false")
	}

	// Insecure creds with RequireTransportSecurity == false allow insecure (for bufconn tests).
	insecWrapperAllowed := tokenCredWrapper{
		token:         StaticTokenSource{Token: "nsct_bufconn_secret"},
		allowInsecure: shouldAllowInsecurePerRPCCreds(insecCreds),
	}
	if insecWrapperAllowed.RequireTransportSecurity() {
		t.Fatal("insecure creds with RequireTransportSecurity == false should return RequireTransportSecurity() == false")
	}

	// 2. When RequireTransportSecurity is true, even insecure creds enforce RequireTransportSecurity() == true.
	RequireTransportSecurity = true
	insecWrapperBlocked := tokenCredWrapper{
		token:         StaticTokenSource{Token: "nsct_blocked_secret"},
		allowInsecure: shouldAllowInsecurePerRPCCreds(insecCreds),
	}
	if !insecWrapperBlocked.RequireTransportSecurity() {
		t.Fatal("insecure creds with RequireTransportSecurity == true must require transport security")
	}

	// Verify NewConnectionWithEndpointWithTransportCredentials rejects insecure per-RPC credentials when RequireTransportSecurity == true.
	_, err := NewConnectionWithEndpointWithTransportCredentials(
		context.Background(),
		"https://127.0.0.1:443",
		StaticTokenSource{Token: "nsct_blocked"},
		insecure.NewCredentials(),
	)
	if err == nil || !strings.Contains(err.Error(), "transport level security") {
		t.Fatalf("expected transport level security error when dialing insecure with RequireTransportSecurity=true, got: %v", err)
	}

	// 3. Verify end-to-end TLS gRPC connection succeeds with StaticTokenSource while RequireTransportSecurity == false.
	RequireTransportSecurity = false
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

type testEchoSessionServer struct {
	sessionsv1betagrpc.UnimplementedUserSessionsServiceServer
	gotAuth     string
	returnEmpty bool
}

func (s *testEchoSessionServer) IssueTenantTokenFromSession(ctx context.Context, _ *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			s.gotAuth = vals[0]
		}
	}
	if s.returnEmpty {
		return &sessions.IssueTenantTokenFromSessionResponse{TenantToken: "   "}, nil
	}
	return &sessions.IssueTenantTokenFromSessionResponse{TenantToken: "nsct_exchanged_tls_token"}, nil
}

func TestSessionTokenSource_EnforcesTransportSecurityAndRejectsEmptyToken(t *testing.T) {
	orig := RequireTransportSecurity
	t.Cleanup(func() {
		RequireTransportSecurity = orig
	})

	// 1. With RequireTransportSecurity=true and insecure creds, SessionTokenSource MUST refuse to send session tokens in cleartext.
	RequireTransportSecurity = true
	insecSTS := NewSessionTokenSource("st_cleartext_must_not_send", "https://127.0.0.1:443", insecure.NewCredentials())
	defer func() { _ = insecSTS.Close() }()

	_, err := insecSTS.IssueToken(context.Background(), time.Minute, false)
	if err == nil || !strings.Contains(err.Error(), "transport level security") {
		t.Fatalf("expected transport level security error for insecure SessionTokenSource with RequireTransportSecurity=true, got: %v", err)
	}

	// 2. Over TLS 1.2+, SessionTokenSource delivers session token via per-RPC credentials and rejects empty responses.
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
	sessSrv := &testEchoSessionServer{}
	sessionsv1betagrpc.RegisterUserSessionsServiceServer(srv, sessSrv)
	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)

	clientTLS := newDefaultTLSConfig()
	clientTLS.RootCAs = rootPool

	tlsSTS := NewSessionTokenSource("st_tls_session_secret", "https://"+lis.Addr().String(), credentials.NewTLS(clientTLS))
	defer func() { _ = tlsSTS.Close() }()

	tok, err := tlsSTS.IssueToken(context.Background(), time.Minute, false)
	if err != nil {
		t.Fatalf("expected TLS SessionTokenSource.IssueToken to succeed, got: %v", err)
	}
	if tok != "nsct_exchanged_tls_token" {
		t.Fatalf("expected exchanged token, got %q", tok)
	}
	if sessSrv.gotAuth != "Bearer st_tls_session_secret" {
		t.Fatalf("expected server to receive per-RPC Bearer session token, got %q", sessSrv.gotAuth)
	}

	// Empty token response must fail closed instead of caching empty token.
	sessSrv.returnEmpty = true
	if _, err := tlsSTS.IssueToken(context.Background(), time.Minute, true); err == nil || !strings.Contains(err.Error(), "empty tenant token") {
		t.Fatalf("expected empty tenant token error, got: %v", err)
	}
}

func TestSSRFSafeDialer_BlocksMetadataIPsAndPreservesValidatedHTTPSProxy(t *testing.T) {
	// 1. Direct dial-time Control rejects blocked metadata/link-local/unspecified/NAT64 IPs.
	blockedAddrs := []string{
		"169.254.169.254:443",
		"169.254.0.1:443",
		"[fe80::1]:443",
		"[fd00:ec2::254]:443",
		"100.100.100.200:443",
		"[::169.254.169.254]:443",
		"[64:ff9b::169.254.169.254]:443",
		"0.0.0.0:443",
		"[::]:443",
	}
	for _, addr := range blockedAddrs {
		if err := ssrfDialControl("tcp", addr, nil); err == nil {
			t.Errorf("expected ssrfDialControl to reject %q, got nil", addr)
		}
	}

	allowedAddrs := []string{
		"127.0.0.1:443",
		"[::1]:443",
		"10.0.0.1:443",
		"203.0.113.10:443",
	}
	for _, addr := range allowedAddrs {
		if err := ssrfDialControl("tcp", addr, nil); err != nil {
			t.Errorf("expected ssrfDialControl to allow %q, got: %v", addr, err)
		}
	}

	// 2. HTTPS_PROXY targeting IMDS IP or cloud metadata hostname is rejected immediately.
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	for _, badProxy := range []string{
		"http://169.254.169.254:80",
		"http://metadata.google.internal:80",
		"http://100.100.100.200:80",
		"socks5://127.0.0.1:1080",
	} {
		t.Setenv("HTTPS_PROXY", badProxy)
		t.Setenv("https_proxy", badProxy)
		if _, err := NewConnectionWithEndpointWithTransportCredentials(
			context.Background(),
			"https://global.namespaceapis.com:443",
			StaticTokenSource{Token: "nsct_test"},
			nil,
		); err == nil {
			t.Fatalf("expected bad HTTPS_PROXY %q to be rejected, got nil", badProxy)
		}
	}

	// 3. Valid corporate HTTP CONNECT proxy on loopback tunnels TLS gRPC end-to-end
	// for a non-loopback hostname (httpproxy bypasses proxies for loopback targets).
	const proxyTargetHost = "proxy-target.example.test"
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate proxy target key: %v", err)
	}
	targetTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: proxyTargetHost},
		DNSNames:     []string{proxyTargetHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	targetCertDER, err := x509.CreateCertificate(rand.Reader, targetTemplate, targetTemplate, &privKey.PublicKey, privKey)
	if err != nil {
		t.Fatalf("failed to create proxy target cert: %v", err)
	}
	targetKeyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		t.Fatalf("failed to marshal proxy target key: %v", err)
	}
	targetCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: targetCertDER})
	targetKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: targetKeyDER})
	tlsCert, err := tls.X509KeyPair(targetCertPEM, targetKeyPEM)
	if err != nil {
		t.Fatalf("failed to load proxy target X509KeyPair: %v", err)
	}
	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(targetCertPEM) {
		t.Fatal("failed to append proxy target certificate to root pool")
	}

	targetLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen for target gRPC server: %v", err)
	}
	defer func() { _ = targetLis.Close() }()

	grpcSrv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{tlsCert},
	})))
	echoSrv := &testEchoTokenServer{}
	iamv1betagrpc.RegisterTokenServiceServer(grpcSrv, echoSrv)
	go func() {
		_ = grpcSrv.Serve(targetLis)
	}()
	t.Cleanup(grpcSrv.Stop)

	proxyLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen for test CONNECT proxy: %v", err)
	}
	defer func() { _ = proxyLis.Close() }()

	var connectCount atomic.Int32
	go func() {
		for {
			clientConn, acceptErr := proxyLis.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				br := bufio.NewReader(c)
				req, reqErr := http.ReadRequest(br)
				if reqErr != nil || req.Method != http.MethodConnect {
					return
				}
				if req.Host != proxyTargetHost+":443" {
					_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\n\r\n")
					return
				}
				connectCount.Add(1)
				upstream, dialErr := net.DialTimeout("tcp", targetLis.Addr().String(), 2*time.Second)
				if dialErr != nil {
					_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer func() { _ = upstream.Close() }()
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
				done := make(chan struct{}, 2)
				go func() {
					_, _ = io.Copy(upstream, br)
					done <- struct{}{}
				}()
				go func() {
					_, _ = io.Copy(c, upstream)
					done <- struct{}{}
				}()
				<-done
			}(clientConn)
		}
	}()

	proxyAddr := "http://" + proxyLis.Addr().String()
	t.Setenv("HTTPS_PROXY", proxyAddr)
	t.Setenv("https_proxy", proxyAddr)

	clientTLS := newDefaultTLSConfig()
	clientTLS.RootCAs = rootPool
	conn, err := NewConnectionWithEndpointWithTransportCredentials(
		context.Background(),
		"https://"+proxyTargetHost+":443",
		StaticTokenSource{Token: "nsct_proxied_tls_token"},
		credentials.NewTLS(clientTLS),
	)
	if err != nil {
		t.Fatalf("failed to create proxied TLS gRPC client: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tokCli := iamv1betagrpc.NewTokenServiceClient(conn)
	if _, err := tokCli.IssueTenantToken(ctx, &iamv1beta.IssueTenantTokenRequest{}); err != nil {
		t.Fatalf("proxied TLS gRPC call failed: %v", err)
	}
	if connectCount.Load() == 0 {
		t.Fatal("expected gRPC connection to traverse the configured HTTP CONNECT proxy")
	}
	if echoSrv.gotAuth != "Bearer nsct_proxied_tls_token" {
		t.Fatalf("expected server to receive Bearer token via proxy tunnel, got %q", echoSrv.gotAuth)
	}
}

func TestHTTPConnectHandshake_ProxyAuth_Non200_BufferedConn_And_HTTPSProxy(t *testing.T) {
	t.Run("Proxy-Authorization Basic header and bufferedConn.Read", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = serverConn.Close() }()

		gotProxyAuth := make(chan string, 1)
		go func() {
			br := bufio.NewReader(serverConn)
			req, err := http.ReadRequest(br)
			if err != nil {
				gotProxyAuth <- "READ_ERR:" + err.Error()
				return
			}
			gotProxyAuth <- req.Header.Get("Proxy-Authorization")
			_, _ = io.WriteString(serverConn, "HTTP/1.1 200 Connection Established\r\n\r\nBUFFERED_PREFETCH:")
			_, _ = io.WriteString(serverConn, "REST")
			_ = serverConn.Close()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tunneled, err := performHTTPConnectHandshake(ctx, clientConn, "global.namespaceapis.com:443", "nsc-go/1", url.UserPassword("proxyuser", "proxypass"))
		if err != nil {
			t.Fatalf("performHTTPConnectHandshake failed: %v", err)
		}
		if authHdr := <-gotProxyAuth; authHdr != "Basic cHJveHl1c2VyOnByb3h5cGFzcw==" {
			t.Fatalf("expected Basic Proxy-Authorization header, got %q", authHdr)
		}
		if _, ok := tunneled.(*bufferedConn); !ok {
			t.Fatalf("expected *bufferedConn when response includes prefetched bytes, got %T", tunneled)
		}
		payload, readErr := io.ReadAll(tunneled)
		if readErr != nil {
			t.Fatalf("bufferedConn.Read failed: %v", readErr)
		}
		if string(payload) != "BUFFERED_PREFETCH:REST" {
			t.Fatalf("expected buffered + socket payload %q, got %q", "BUFFERED_PREFETCH:REST", string(payload))
		}
	})

	t.Run("Non-200 HTTP CONNECT response is rejected", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = serverConn.Close() }()

		go func() {
			br := bufio.NewReader(serverConn)
			_, _ = http.ReadRequest(br)
			_, _ = io.WriteString(serverConn, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := performHTTPConnectHandshake(ctx, clientConn, "global.namespaceapis.com:443", "nsc-go/1", nil)
		if err == nil || !strings.Contains(err.Error(), "407") {
			t.Fatalf("expected 407 HTTP CONNECT rejection, got: %v", err)
		}
	})

	t.Run("HTTPS proxy dialer performs TLS handshake and rejects blocked proxy host", func(t *testing.T) {
		blockedDial := newProxySSRFSafeDialer(&url.URL{Scheme: "https", Host: "169.254.169.254"}, "global.namespaceapis.com:443", "nsc-go/1")
		if _, err := blockedDial(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "proxy host rejected") {
			t.Fatalf("expected blocked HTTPS proxy host error, got: %v", err)
		}

		tlsCert, _ := generateLoopbackTestCert(t)
		ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{tlsCert},
		})
		if err != nil {
			t.Fatalf("failed to start TLS proxy listener: %v", err)
		}
		defer func() { _ = ln.Close() }()

		go func() {
			conn, acceptErr := ln.Accept()
			if acceptErr == nil && conn != nil {
				_ = conn.(*tls.Conn).Handshake()
				_ = conn.Close()
			}
		}()

		proxyURL := &url.URL{Scheme: "https", Host: ln.Addr().String()}
		dialFn := newProxySSRFSafeDialer(proxyURL, "global.namespaceapis.com:443", "nsc-go/1")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := dialFn(ctx, ""); err == nil || !strings.Contains(err.Error(), "proxy TLS handshake failed") {
			t.Fatalf("expected proxy TLS handshake error for untrusted test cert, got: %v", err)
		}
	})
}

func TestRedactingUnaryInterceptor_MultilineJSONEscapedSecrets(t *testing.T) {
	t.Parallel()

	sampleJWT := "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"ey" + "JzdWIiOiIxMjM0NTY3ODkwIn0." +
		"SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	specFileSecret := "/run/secrets/multiline_spec.json"
	passwordSecret := "multiline_pw_secret"
	nsrtSecret := "nsrt_" + "multiline_revokable_123456"
	oidcJWT := "oidc_" + sampleJWT
	dockerfilePass := "docker_build_pw_999"

	req := &githubv1beta.CreateProfileRequest{
		Spec: &githubv1beta.RunnerProfileSpec{
			Tag: "probe",
			Description: strings.Join([]string{
				"line1",
				"NSC_TOKEN_SPEC_FILE=" + specFileSecret,
				"password=" + passwordSecret,
				nsrtSecret,
				oidcJWT,
				"https://example.invalid/auth?token=tok123&password=" + passwordSecret + "&safe=1",
			}, "\n"),
			CustomRunnerImageSpec: &githubv1beta.CustomRunnerImageSpec{
				DockerSpec: &githubv1beta.DockerBaseImageSpec{
					DockerfileContent: "FROM alpine:3.19\nENV password=" + dockerfilePass + "\nENV " + nsrtSecret + "\n",
				},
			},
		},
	}

	var logBuf bytes.Buffer
	syncWriter := NewSynchronizedRedactingWriter(&logBuf)
	interceptor := RedactingUnaryInterceptor(syncWriter, true, false, "https://global.namespaceapis.com:443")

	err := interceptor(
		context.Background(),
		"/namespace.cloud.github.v1beta.ProfileService/CreateProfile",
		req,
		&githubv1beta.CreateProfileResponse{},
		nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return nil
		},
	)
	if err != nil {
		t.Fatalf("interceptor returned unexpected error: %v", err)
	}

	logged := logBuf.String()
	for _, secret := range []string{
		specFileSecret,
		passwordSecret,
		nsrtSecret,
		oidcJWT,
		sampleJWT,
		dockerfilePass,
		"tok123",
	} {
		if strings.Contains(logged, secret) {
			t.Fatalf("SECURITY LEAK: RedactingUnaryInterceptor logged raw secret %q in:\n%s", secret, logged)
		}
	}

	var payloadLine string
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		if strings.HasPrefix(line, "[DEBUG] Payload: ") {
			payloadLine = strings.TrimPrefix(line, "[DEBUG] Payload: ")
			break
		}
	}
	if payloadLine == "" {
		t.Fatalf("expected [DEBUG] Payload line in:\n%s", logged)
	}
	if !json.Valid([]byte(payloadLine)) {
		t.Fatalf("redacted [DEBUG] Payload JSON is invalid: %s", payloadLine)
	}
	if !strings.Contains(payloadLine, "NSC_TOKEN_SPEC_FILE=[REDACTED]") ||
		!strings.Contains(payloadLine, "[REDACTED TOKEN]") ||
		!strings.Contains(payloadLine, "[REDACTED JWT]") ||
		!strings.Contains(payloadLine, "password=[REDACTED PASSWORD]") ||
		!strings.Contains(payloadLine, "safe=1") {
		t.Fatalf("expected redaction markers and preserved safe=1 param in payload JSON: %s", payloadLine)
	}
	if second := RedactSecrets(logged); second != logged {
		t.Fatalf("logged interceptor output is not 2-pass idempotent:\nfirst:  %s\nsecond: %s", logged, second)
	}
}
