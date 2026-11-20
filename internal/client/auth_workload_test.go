package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/private/sessions/sessionsv1betagrpc"
	sessions "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/sessions"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

type workloadTestSessionsServer struct {
	sessionsv1betagrpc.UnimplementedUserSessionsServiceServer
	calls    atomic.Int64
	lastAuth string
}

func (s *workloadTestSessionsServer) IssueTenantTokenFromSession(ctx context.Context, _ *sessions.IssueTenantTokenFromSessionRequest) (*sessions.IssueTenantTokenFromSessionResponse, error) {
	s.calls.Add(1)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			s.lastAuth = vals[0]
		}
	}
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	pay := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"tenant_id":"win-workload-tenant","exp":%d}`, time.Now().Add(time.Hour).Unix())))
	return &sessions.IssueTenantTokenFromSessionResponse{
		TenantToken: "nsct_" + hdr + "." + pay + ".sig",
	}, nil
}

func TestWorkloadTokenPaths_CrossPlatform(t *testing.T) {
	t.Run("Unix platforms always return /var/run/nsc/token.json regardless of Windows env vars", func(t *testing.T) {
		getenv := func(k string) string {
			switch k {
			case "NSC_METADATA_DIR":
				return `C:\nsc\meta`
			case "ProgramData":
				return `C:\ProgramData`
			default:
				return ""
			}
		}
		for _, goos := range []string{"linux", "darwin"} {
			got := workloadTokenPaths(goos, getenv)
			want := []string{"/var/run/nsc/token.json"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("workloadTokenPaths(%q) = %v, want %v", goos, got, want)
			}
		}
	})

	t.Run("Windows returns NSC_METADATA_DIR then ProgramData GuestAgent path", func(t *testing.T) {
		getenv := func(k string) string {
			switch k {
			case "NSC_METADATA_DIR":
				return `C:\nsc\meta`
			case "ProgramData":
				return `C:\ProgramData`
			default:
				return ""
			}
		}
		got := workloadTokenPaths("windows", getenv)
		want := []string{
			`C:\nsc\meta\token.json`,
			`C:\ProgramData\Namespace\GuestAgent\metadata\token.json`,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("workloadTokenPaths(windows) = %v, want %v", got, want)
		}
	})

	t.Run("Windows omits empty/whitespace NSC_METADATA_DIR", func(t *testing.T) {
		getenv := func(k string) string {
			switch k {
			case "NSC_METADATA_DIR":
				return "   \t "
			case "ProgramData":
				return `C:\ProgramData\`
			default:
				return ""
			}
		}
		got := workloadTokenPaths("windows", getenv)
		want := []string{`C:\ProgramData\Namespace\GuestAgent\metadata\token.json`}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("workloadTokenPaths(windows) = %v, want %v", got, want)
		}
	})

	t.Run("Windows returns empty slice when neither env var is set", func(t *testing.T) {
		got := workloadTokenPaths("windows", func(string) string { return "" })
		if len(got) != 0 {
			t.Fatalf("expected empty workload paths when both env vars unset, got %v", got)
		}
	})

	t.Run("joinPathForOS preserves single leading backslash for root-only Windows paths", func(t *testing.T) {
		cases := []struct {
			elem []string
			want string
		}{
			{elem: []string{`\`, "token.json"}, want: `\token.json`},
			{elem: []string{`/`, "token.json"}, want: `\token.json`},
			{elem: []string{`\`}, want: `\`},
			{elem: []string{`/`}, want: `\`},
			{elem: []string{`C:\`, "token.json"}, want: `C:\token.json`},
			{elem: []string{`C:\ProgramData\`, "Namespace", "token.json"}, want: `C:\ProgramData\Namespace\token.json`},
		}
		for _, tc := range cases {
			got := joinPathForOS("windows", tc.elem...)
			if got != tc.want {
				t.Fatalf("joinPathForOS(windows, %v) = %q, want %q", tc.elem, got, tc.want)
			}
		}
	})
}

func TestLoadDefaultTokenSourceWith_WindowsResolutionAndErrors(t *testing.T) {
	t.Run("NSC_METADATA_DIR takes precedence over ProgramData and userConfigDir", func(t *testing.T) {
		root := t.TempDir()
		metaDir := filepath.Join(root, "meta")
		progData := filepath.Join(root, "ProgramData")
		userCfg := filepath.Join(root, "AppData")

		metaFile := filepath.Join(metaDir, "token.json")
		progFile := filepath.Join(progData, "Namespace", "GuestAgent", "metadata", "token.json")
		userFile := filepath.Join(userCfg, "ns", "token.json")

		for path, tok := range map[string]string{
			metaFile: "nsct_" + "from_meta_dir",
			progFile: "nsct_" + "from_prog_data",
			userFile: "nsct_" + "from_user_cfg",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"bearer_token":"`+tok+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}

		getenv := func(k string) string {
			switch k {
			case "NSC_METADATA_DIR":
				return metaDir
			case "ProgramData":
				return progData
			default:
				return ""
			}
		}
		ts, err := loadDefaultTokenSourceWith("windows", getenv, func() (string, error) { return userCfg, nil }, "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lts, ok := ts.(*LocalTokenSource)
		if !ok || lts.FilePath() != metaFile {
			t.Fatalf("expected LocalTokenSource(%q), got %T (%v)", metaFile, ts, ts)
		}
	})

	t.Run("Raw st_ workload credential file on Windows is validated and exchanged", func(t *testing.T) {
		origRTS := RequireTransportSecurity
		RequireTransportSecurity = false
		t.Cleanup(func() { RequireTransportSecurity = origRTS })

		lis := bufconn.Listen(1024 * 1024)
		srv := grpc.NewServer()
		mock := &workloadTestSessionsServer{}
		sessionsv1betagrpc.RegisterUserSessionsServiceServer(srv, mock)
		go func() { _ = srv.Serve(lis) }()
		t.Cleanup(func() {
			srv.Stop()
			_ = lis.Close()
		})

		root := t.TempDir()
		metaDir := filepath.Join(root, "meta")
		metaFile := filepath.Join(metaDir, "token.json")
		if err := os.MkdirAll(metaDir, 0700); err != nil {
			t.Fatal(err)
		}
		rawST := "st_" + "win_workload_raw_session_987654"
		if err := os.WriteFile(metaFile, []byte(rawST+"\r\n"), 0600); err != nil {
			t.Fatal(err)
		}

		getenv := func(k string) string {
			if k == "NSC_METADATA_DIR" {
				return metaDir
			}
			return ""
		}
		dialOpt := grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		})
		ts, err := loadDefaultTokenSourceWith("windows", getenv, func() (string, error) { return root, nil }, "https://127.0.0.1:19443", insecure.NewCredentials(), dialOpt)
		if err != nil {
			t.Fatalf("expected raw st_ workload file to succeed, got: %v", err)
		}
		lts, ok := ts.(*LocalTokenSource)
		if !ok || lts.FilePath() != metaFile {
			t.Fatalf("expected LocalTokenSource(%q), got %T (%v)", metaFile, ts, ts)
		}
		t.Cleanup(func() { _ = lts.Close() })

		tok, err := ts.IssueToken(context.Background(), time.Minute, false)
		if err != nil {
			t.Fatalf("expected raw st_ workload IssueToken to succeed, got: %v", err)
		}
		if !strings.HasPrefix(tok, "nsct_") || mock.calls.Load() != 1 {
			t.Fatalf("expected exchanged nsct_ token with 1 call, got tok=%q calls=%d", tok, mock.calls.Load())
		}
		if mock.lastAuth != "Bearer "+rawST {
			t.Fatalf("expected Bearer %q, got %q", rawST, mock.lastAuth)
		}
	})

	t.Run("Missing NSC_METADATA_DIR token falls back to ProgramData GuestAgent path", func(t *testing.T) {
		root := t.TempDir()
		metaDir := filepath.Join(root, "missing-meta")
		progData := filepath.Join(root, "ProgramData")
		userCfg := filepath.Join(root, "AppData")

		progFile := filepath.Join(progData, "Namespace", "GuestAgent", "metadata", "token.json")
		userFile := filepath.Join(userCfg, "ns", "token.json")

		for path, tok := range map[string]string{
			progFile: "nsct_" + "from_prog_data",
			userFile: "nsct_" + "from_user_cfg",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"bearer_token":"`+tok+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}

		getenv := func(k string) string {
			switch k {
			case "NSC_METADATA_DIR":
				return metaDir
			case "ProgramData":
				return progData
			default:
				return ""
			}
		}
		ts, err := loadDefaultTokenSourceWith("windows", getenv, func() (string, error) { return userCfg, nil }, "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lts, ok := ts.(*LocalTokenSource)
		if !ok || lts.FilePath() != progFile {
			t.Fatalf("expected LocalTokenSource(%q), got %T (%v)", progFile, ts, ts)
		}
	})

	t.Run("Missing both workload paths falls back to userConfigDir/ns/token.json", func(t *testing.T) {
		root := t.TempDir()
		userCfg := filepath.Join(root, "AppData")
		userFile := filepath.Join(userCfg, "ns", "token.json")
		if err := os.MkdirAll(filepath.Dir(userFile), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(userFile, []byte(`{"bearer_token":"`+"nsct_"+"from_user_cfg"+`"}`), 0600); err != nil {
			t.Fatal(err)
		}

		getenv := func(k string) string {
			switch k {
			case "NSC_METADATA_DIR":
				return filepath.Join(root, "absent-meta")
			case "ProgramData":
				return filepath.Join(root, "absent-prog")
			default:
				return ""
			}
		}
		ts, err := loadDefaultTokenSourceWith("windows", getenv, func() (string, error) { return userCfg, nil }, "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		lts, ok := ts.(*LocalTokenSource)
		if !ok || lts.FilePath() != userFile {
			t.Fatalf("expected LocalTokenSource(%q), got %T (%v)", userFile, ts, ts)
		}
	})

	t.Run("Malformed workload file fails eagerly without falling back", func(t *testing.T) {
		root := t.TempDir()
		metaDir := filepath.Join(root, "meta")
		progData := filepath.Join(root, "ProgramData")
		userCfg := filepath.Join(root, "AppData")

		metaFile := filepath.Join(metaDir, "token.json")
		progFile := filepath.Join(progData, "Namespace", "GuestAgent", "metadata", "token.json")
		userFile := filepath.Join(userCfg, "ns", "token.json")

		if err := os.MkdirAll(metaDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metaFile, []byte("not-json"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, validPath := range []string{progFile, userFile} {
			if err := os.MkdirAll(filepath.Dir(validPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(validPath, []byte(`{"bearer_token":"`+"nsct_"+"valid"+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}

		getenv := func(k string) string {
			switch k {
			case "NSC_METADATA_DIR":
				return metaDir
			case "ProgramData":
				return progData
			default:
				return ""
			}
		}
		_, err := loadDefaultTokenSourceWith("windows", getenv, func() (string, error) { return userCfg, nil }, "", nil)
		if err == nil || !strings.Contains(err.Error(), "default workload credential") || !strings.Contains(err.Error(), "malformed JSON") {
			t.Fatalf("expected eager malformed workload credential error, got: %v", err)
		}
	})

	t.Run("Directory at workload credential path fails eagerly", func(t *testing.T) {
		root := t.TempDir()
		metaDir := filepath.Join(root, "meta")
		metaFileAsDir := filepath.Join(metaDir, "token.json")
		if err := os.MkdirAll(metaFileAsDir, 0700); err != nil {
			t.Fatal(err)
		}

		getenv := func(k string) string {
			if k == "NSC_METADATA_DIR" {
				return metaDir
			}
			return ""
		}
		_, err := loadDefaultTokenSourceWith("windows", getenv, func() (string, error) { return root, nil }, "", nil)
		if err == nil || !strings.Contains(err.Error(), "is a directory") {
			t.Fatalf("expected directory error, got: %v", err)
		}
	})

	t.Run("Non-NotExist stat error returns failed to inspect default workload credential", func(t *testing.T) {
		root := t.TempDir()
		regularFileAsMetaDir := filepath.Join(root, "not-a-dir")
		if err := os.WriteFile(regularFileAsMetaDir, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}

		getenv := func(k string) string {
			if k == "NSC_METADATA_DIR" {
				return regularFileAsMetaDir
			}
			return ""
		}
		_, err := loadDefaultTokenSourceWith("windows", getenv, func() (string, error) { return root, nil }, "", nil)
		if err == nil || !strings.Contains(err.Error(), "failed to inspect default workload credential") {
			t.Fatalf("expected inspect error for ENOTDIR path, got: %v", err)
		}
	})

	t.Run("UserConfigDir error returns descriptive error", func(t *testing.T) {
		_, err := loadDefaultTokenSourceWith("windows", func(string) string { return "" }, func() (string, error) {
			return "", fmt.Errorf("no home dir")
		}, "", nil)
		if err == nil || !strings.Contains(err.Error(), "operating-system user configuration directory") {
			t.Fatalf("expected userConfigDir error, got: %v", err)
		}
	})

	t.Run("User credential path as directory fails eagerly", func(t *testing.T) {
		root := t.TempDir()
		userCfg := filepath.Join(root, "AppData")
		userTokenAsDir := filepath.Join(userCfg, "ns", "token.json")
		if err := os.MkdirAll(userTokenAsDir, 0700); err != nil {
			t.Fatal(err)
		}

		_, err := loadDefaultTokenSourceWith("windows", func(string) string { return "" }, func() (string, error) {
			return userCfg, nil
		}, "", nil)
		if err == nil || !strings.Contains(err.Error(), "default user credential") || !strings.Contains(err.Error(), "is a directory") {
			t.Fatalf("expected default user credential directory error, got: %v", err)
		}
	})

	t.Run("User credential ENOTDIR stat error returns failed to inspect", func(t *testing.T) {
		root := t.TempDir()
		userCfg := filepath.Join(root, "AppData")
		nsAsFile := filepath.Join(userCfg, "ns")
		if err := os.MkdirAll(userCfg, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(nsAsFile, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}

		_, err := loadDefaultTokenSourceWith("windows", func(string) string { return "" }, func() (string, error) {
			return userCfg, nil
		}, "", nil)
		if err == nil || !strings.Contains(err.Error(), "failed to inspect default user credential") {
			t.Fatalf("expected user credential inspect error, got: %v", err)
		}
	})

	t.Run("Malformed user credential returns validation error", func(t *testing.T) {
		root := t.TempDir()
		userCfg := filepath.Join(root, "AppData")
		userFile := filepath.Join(userCfg, "ns", "token.json")
		if err := os.MkdirAll(filepath.Dir(userFile), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(userFile, []byte("not-json"), 0600); err != nil {
			t.Fatal(err)
		}

		_, err := loadDefaultTokenSourceWith("windows", func(string) string { return "" }, func() (string, error) {
			return userCfg, nil
		}, "", nil)
		if err == nil || !strings.Contains(err.Error(), "default user credential") || !strings.Contains(err.Error(), "malformed JSON") {
			t.Fatalf("expected malformed default user credential error, got: %v", err)
		}
	})
}
