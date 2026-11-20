package client_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

func TestTokenFile_BoundedRead_Max1MiB(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	const maxBytes = 1 << 20

	// 1. Exact 1 MiB file succeeds (padded JSON whitespace around valid token payload)
	jwt := createTestJWT(map[string]interface{}{"tenant_id": "exact-1mib-tenant"})
	prefix := fmt.Sprintf(`{"bearer_token":%q}`, jwt)
	if len(prefix) > maxBytes {
		t.Fatalf("unexpectedly large prefix: %d", len(prefix))
	}
	exactPayload := prefix + strings.Repeat(" ", maxBytes-len(prefix))
	exactPath := filepath.Join(tmpDir, "exact_1mib.json")
	if err := os.WriteFile(exactPath, []byte(exactPayload), 0600); err != nil {
		t.Fatalf("failed to write exact 1 MiB file: %v", err)
	}

	ts, err := client.ResolveTokenSource(ctx, client.AuthConfig{TokenFile: exactPath})
	if err != nil {
		t.Fatalf("expected exact 1 MiB file to succeed in ResolveTokenSource, got: %v", err)
	}
	tok, err := ts.IssueToken(ctx, 0, false)
	if err != nil || tok != jwt {
		t.Fatalf("expected exact 1 MiB IssueToken to succeed, got tok=%q err=%v", tok, err)
	}
	tenant, err := client.ResolveTenantFromToken(exactPath)
	if err != nil || tenant != "exact-1mib-tenant" {
		t.Fatalf("expected exact 1 MiB ResolveTenantFromToken to succeed, got tenant=%q err=%v", tenant, err)
	}

	// 2. Oversized (1 MiB + 1 byte) file is rejected in validateTokenFile, IssueToken, and ResolveTenantFromToken
	overPayload := prefix + strings.Repeat(" ", maxBytes-len(prefix)+1)
	overPath := filepath.Join(tmpDir, "oversized.json")
	if err := os.WriteFile(overPath, []byte(overPayload), 0600); err != nil {
		t.Fatalf("failed to write oversized file: %v", err)
	}

	if _, err := client.ResolveTokenSource(ctx, client.AuthConfig{TokenFile: overPath}); err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed size") {
		t.Fatalf("expected ResolveTokenSource size limit error, got: %v", err)
	}

	// Simulate file growing past 1 MiB after initial ResolveTokenSource validation
	rotatingPath := filepath.Join(tmpDir, "rotating.json")
	if err := os.WriteFile(rotatingPath, []byte(prefix), 0600); err != nil {
		t.Fatalf("failed to write initial rotating file: %v", err)
	}
	rotatingTS, err := client.ResolveTokenSource(ctx, client.AuthConfig{TokenFile: rotatingPath})
	if err != nil {
		t.Fatalf("unexpected error resolving rotating file: %v", err)
	}
	if err := os.WriteFile(rotatingPath, []byte(overPayload), 0600); err != nil {
		t.Fatalf("failed to overwrite rotating file with oversized content: %v", err)
	}
	if _, err := rotatingTS.IssueToken(ctx, 0, false); err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed size") {
		t.Fatalf("expected LocalTokenSource.IssueToken size limit error, got: %v", err)
	}

	if _, err := client.ResolveTenantFromToken(overPath); err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed size") {
		t.Fatalf("expected ResolveTenantFromToken size limit error, got: %v", err)
	}
}

func TestResolveTenantFromToken_InlinePrefixesNeverProbeFilesystem(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	diskJWT := createTestJWT(map[string]interface{}{"tenant_id": "disk-tenant"})
	diskFileContent := []byte(fmt.Sprintf(`{"bearer_token": %q}`, diskJWT))

	inlinePrefixes := []string{
		"nsct_opaque_probe_test",
		"st_opaque_probe_test",
		"nscw_opaque_probe_test",
		"nscu_opaque_probe_test",
		"tok_opaque_probe_test",
	}
	for _, name := range inlinePrefixes {
		if err := os.WriteFile(filepath.Join(tmpDir, name), diskFileContent, 0600); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
		tenant, err := client.ResolveTenantFromToken(name)
		if err == nil {
			t.Fatalf("expected inline token %q to fail closed without reading file on disk (got tenant=%q)", name, tenant)
		}
		if !strings.Contains(err.Error(), "unsupported or opaque token format") {
			t.Fatalf("expected opaque format error for %q, got: %v", name, err)
		}
	}

	// 3-part eyJ JWT matching a filename in CWD must parse the inline JWT, NOT the file on disk
	inlineJWT := createTestJWT(map[string]interface{}{"tenant_id": "inline-tenant"})
	if err := os.WriteFile(filepath.Join(tmpDir, inlineJWT), diskFileContent, 0600); err != nil {
		t.Fatalf("failed to write JWT-named file: %v", err)
	}
	tenant, err := client.ResolveTenantFromToken(inlineJWT)
	if err != nil {
		t.Fatalf("expected inline JWT to succeed, got: %v", err)
	}
	if tenant != "inline-tenant" {
		t.Fatalf("expected inline-tenant (without reading file on disk), got %q", tenant)
	}
}

func TestCheckTokenFilePermissions_AndClientTokenFilePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()

	strictPath := filepath.Join(tmpDir, "strict.json")
	if err := os.WriteFile(strictPath, []byte(`{"bearer_token":"nsct_strict"}`), 0600); err != nil {
		t.Fatalf("failed to write strict token file: %v", err)
	}
	if warn := client.CheckTokenFilePermissions(strictPath); warn != "" {
		t.Errorf("expected empty warning for 0600 file, got: %s", warn)
	}

	roPath := filepath.Join(tmpDir, "readonly.json")
	if err := os.WriteFile(roPath, []byte(`{"bearer_token":"nsct_ro"}`), 0400); err != nil {
		t.Fatalf("failed to write readonly token file: %v", err)
	}
	if warn := client.CheckTokenFilePermissions(roPath); warn != "" {
		t.Errorf("expected empty warning for 0400 file, got: %s", warn)
	}

	for _, mode := range []os.FileMode{0644, 0640, 0604} {
		permPath := filepath.Join(tmpDir, fmt.Sprintf("perm_%04o.json", mode))
		if err := os.WriteFile(permPath, []byte(`{"bearer_token":"nsct_perm"}`), mode); err != nil {
			t.Fatalf("failed to write %04o token file: %v", mode, err)
		}
		if err := os.Chmod(permPath, mode); err != nil {
			t.Fatalf("failed to chmod %04o: %v", mode, err)
		}
		warn := client.CheckTokenFilePermissions(permPath)
		if warn == "" || !strings.Contains(warn, "0600") {
			t.Errorf("expected permission warning mentioning 0600 for mode %04o, got: %q", mode, warn)
		}
	}

	if warn := client.CheckTokenFilePermissions(tmpDir); warn != "" {
		t.Errorf("expected empty warning for directory, got: %q", warn)
	}
	if warn := client.CheckTokenFilePermissions(filepath.Join(tmpDir, "missing.json")); warn != "" {
		t.Errorf("expected empty warning for nonexistent file, got: %q", warn)
	}

	// Verify Client.TokenFilePath() reflects active token source
	ctx := context.Background()
	cliStatic, err := client.New(ctx, client.Config{Token: "nsct_static"})
	if err != nil {
		t.Fatalf("client.New static failed: %v", err)
	}
	defer func() { _ = cliStatic.Close() }()
	if got := cliStatic.TokenFilePath(); got != "" {
		t.Errorf("expected empty TokenFilePath for static token, got %q", got)
	}

	cliFile, err := client.New(ctx, client.Config{TokenFile: strictPath})
	if err != nil {
		t.Fatalf("client.New file failed: %v", err)
	}
	defer func() { _ = cliFile.Close() }()
	if got := cliFile.TokenFilePath(); got != strictPath {
		t.Errorf("expected TokenFilePath %q, got %q", strictPath, got)
	}
}
