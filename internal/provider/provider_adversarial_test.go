package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	nsProvider "github.com/bsuv/terraform-provider-namespace/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"namespacelabs.dev/integrations/nsc/grpcapi"
)

func init() {
	grpcapi.RequireTransportSecurity = false
}

func makeProviderConfig(ctx context.Context, p provider.Provider, values map[string]interface{}) tfsdk.Config {
	schemaResp := &provider.SchemaResponse{}
	p.Schema(ctx, provider.SchemaRequest{}, schemaResp)

	attrTypes := map[string]tftypes.Type{
		"token":            tftypes.String,
		"token_file":       tftypes.String,
		"global_endpoint":  tftypes.String,
		"iam_endpoint":     tftypes.String,
		"compute_endpoint": tftypes.String,
	}

	attrVals := make(map[string]tftypes.Value)
	for k, t := range attrTypes {
		v, exists := values[k]
		if !exists || v == nil {
			attrVals[k] = tftypes.NewValue(t, nil)
		} else if s, ok := v.(string); ok {
			attrVals[k] = tftypes.NewValue(t, s)
		} else {
			attrVals[k] = tftypes.NewValue(t, v)
		}
	}

	val := tftypes.NewValue(tftypes.Object{AttributeTypes: attrTypes}, attrVals)
	return tfsdk.Config{
		Schema: schemaResp.Schema,
		Raw:    val,
	}
}

func TestProvider_Configure_DefersUnknownConfiguration(t *testing.T) {
	t.Setenv("NSC_TOKEN", "")
	t.Setenv("NSC_TOKEN_FILE", "")

	ctx := context.Background()
	for _, attribute := range []string{"token", "token_file", "global_endpoint", "iam_endpoint", "compute_endpoint"} {
		t.Run(attribute, func(t *testing.T) {
			p := nsProvider.New("dev")()
			req := provider.ConfigureRequest{
				Config: makeProviderConfig(ctx, p, map[string]interface{}{
					attribute: tftypes.UnknownValue,
				}),
				ClientCapabilities: provider.ConfigureProviderClientCapabilities{DeferralAllowed: true},
			}
			resp := &provider.ConfigureResponse{}
			p.Configure(ctx, req, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("unknown %s must defer configuration, got errors: %v", attribute, resp.Diagnostics)
			}
			if len(resp.Diagnostics) != 0 {
				t.Fatalf("unknown %s must defer without diagnostics, got: %v", attribute, resp.Diagnostics)
			}
			if resp.Deferred == nil || resp.Deferred.Reason != provider.DeferredReasonProviderConfigUnknown {
				t.Fatalf("unknown %s must return provider-config deferral, got: %#v", attribute, resp.Deferred)
			}
			if resp.ResourceData != nil || resp.DataSourceData != nil || resp.EphemeralResourceData != nil {
				t.Fatalf("unknown %s must not publish a partially configured client", attribute)
			}
		})
	}
}

func TestProvider_Configure_RejectsUnknownConfigurationWithoutDeferral(t *testing.T) {
	t.Setenv("NSC_TOKEN", "")
	t.Setenv("NSC_TOKEN_FILE", "")

	ctx := context.Background()
	p := nsProvider.New("dev")()
	req := provider.ConfigureRequest{
		Config: makeProviderConfig(ctx, p, map[string]interface{}{
			"token": tftypes.UnknownValue,
		}),
	}
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("unknown provider configuration without deferral support must fail closed")
	}
	if resp.Deferred != nil || resp.ResourceData != nil || resp.DataSourceData != nil || resp.EphemeralResourceData != nil {
		t.Fatal("failed provider configuration must not defer or publish a partial client")
	}
}

// TestProvider_SchemaValidation_ConflictsWith tests that token and token_file conflict validators
// correctly flag an error when both attributes are set.
func TestProvider_SchemaValidation_ConflictsWith(t *testing.T) {
	ctx := context.Background()
	p := nsProvider.New("dev")()

	schemaResp := &provider.SchemaResponse{}
	p.Schema(ctx, provider.SchemaRequest{}, schemaResp)

	tokenAttr, exists := schemaResp.Schema.Attributes["token"]
	if !exists {
		t.Fatal("expected 'token' attribute in schema")
	}

	strAttr, ok := tokenAttr.(schema.StringAttribute)
	if !ok {
		t.Fatalf("expected 'token' attribute to be schema.StringAttribute, got %T", tokenAttr)
	}

	tokenValReq := validator.StringRequest{
		Path:           path.Root("token"),
		PathExpression: path.MatchRoot("token"),
		ConfigValue:    types.StringValue("nsct_token"),
		Config: makeProviderConfig(ctx, p, map[string]interface{}{
			"token":      "nsct_token",
			"token_file": "/tmp/token.json",
		}),
	}
	tokenValResp := &validator.StringResponse{}

	for _, v := range strAttr.Validators {
		v.ValidateString(ctx, tokenValReq, tokenValResp)
	}

	if !tokenValResp.Diagnostics.HasError() {
		t.Errorf("expected ConflictsWith validator on 'token' to produce error diagnostics when 'token_file' is also set")
	}
}

// TestProvider_Configure_MissingCredentials verifies that Configure returns an actionable
// error diagnostic when no credentials are provided.
func TestProvider_Configure_MissingCredentials(t *testing.T) {
	t.Setenv("NSC_TOKEN", "")
	t.Setenv("NSC_TOKEN_FILE", "")

	ctx := context.Background()
	p := nsProvider.New("dev")()

	req := provider.ConfigureRequest{
		Config: makeProviderConfig(ctx, p, map[string]interface{}{}),
	}
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected Configure to fail with missing credentials, but got no errors")
	}

	errDiag := resp.Diagnostics[0]
	if errDiag.Summary() != "Failed to Initialize Namespace Client" {
		t.Errorf("expected summary 'Failed to Initialize Namespace Client', got %q", errDiag.Summary())
	}
	if !strings.Contains(errDiag.Detail(), "no Namespace credentials found") && !strings.Contains(errDiag.Detail(), "Provide provider token") {
		t.Errorf("expected actionable detail in diagnostic, got: %s", errDiag.Detail())
	}
}

// TestProvider_Configure_UnreadableTokenFile verifies that Configure reports unreadable token_file.
func TestProvider_Configure_UnreadableTokenFile(t *testing.T) {
	t.Setenv("NSC_TOKEN", "")
	t.Setenv("NSC_TOKEN_FILE", "")

	ctx := context.Background()
	p := nsProvider.New("dev")()

	req := provider.ConfigureRequest{
		Config: makeProviderConfig(ctx, p, map[string]interface{}{
			"token_file": "/nonexistent/path/to/token.json",
		}),
	}
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, req, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected Configure to fail for nonexistent token_file")
	}
	if !strings.Contains(resp.Diagnostics[0].Detail(), "unreadable") {
		t.Errorf("expected 'unreadable' in error detail, got: %s", resp.Diagnostics[0].Detail())
	}
}

// TestProvider_Configure_InvalidEndpoints verifies that Configure validates endpoints.
func TestProvider_Configure_InvalidEndpoints(t *testing.T) {
	ctx := context.Background()
	p := nsProvider.New("dev")()

	cases := []struct {
		name   string
		config map[string]interface{}
		expSub string
	}{
		{
			name: "HTTP Global Endpoint Rejected",
			config: map[string]interface{}{
				"token":           "nsct_token",
				"global_endpoint": "http://global.namespaceapis.com",
			},
			expSub: "http scheme is not supported",
		},
		{
			name: "Path in IAM Endpoint Rejected",
			config: map[string]interface{}{
				"token":        "nsct_token",
				"iam_endpoint": "https://iam.namespaceapis.com/path",
			},
			expSub: "endpoint must not contain path segments",
		},
		{
			name: "Path in Compute Endpoint Rejected",
			config: map[string]interface{}{
				"token":            "nsct_token",
				"compute_endpoint": "eu.compute.namespaceapis.com/segment",
			},
			expSub: "endpoint must not contain path segments",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := provider.ConfigureRequest{
				Config: makeProviderConfig(ctx, p, tc.config),
			}
			resp := &provider.ConfigureResponse{}
			p.Configure(ctx, req, resp)

			if !resp.Diagnostics.HasError() {
				t.Fatalf("expected error for invalid endpoint in %s", tc.name)
			}
			if !strings.Contains(resp.Diagnostics[0].Detail(), tc.expSub) {
				t.Errorf("expected detail containing %q, got %q", tc.expSub, resp.Diagnostics[0].Detail())
			}
		})
	}
}

// TestProvider_Configure_SuccessWithToken verifies successful Configure with token.
func TestProvider_Configure_SuccessWithToken(t *testing.T) {
	ctx := context.Background()
	p := nsProvider.New("v1.2.3")()

	req := provider.ConfigureRequest{
		Config: makeProviderConfig(ctx, p, map[string]interface{}{
			"token": "nsct_valid_test_token_12345",
		}),
	}
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error in Configure: %v", resp.Diagnostics)
	}

	if resp.ResourceData == nil {
		t.Fatal("expected ResourceData to be populated with client")
	}
	if resp.DataSourceData == nil {
		t.Fatal("expected DataSourceData to be populated with client")
	}

	cli, ok := resp.ResourceData.(*client.Client)
	if !ok || cli == nil {
		t.Fatalf("expected ResourceData to be *client.Client, got %T", resp.ResourceData)
	}
	if cli.Pool() == nil {
		t.Fatal("expected EndpointPool to be non-nil")
	}
}

// TestProvider_Configure_SuccessWithTokenFile verifies successful Configure with token_file.
func TestProvider_Configure_SuccessWithTokenFile(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "token.json")
	if err := os.WriteFile(tokenPath, []byte(`{"bearer_token":"nsct_from_file"}`), 0600); err != nil {
		t.Fatalf("failed to write token file: %v", err)
	}

	ctx := context.Background()
	p := nsProvider.New("v1.2.3")()

	req := provider.ConfigureRequest{
		Config: makeProviderConfig(ctx, p, map[string]interface{}{
			"token_file": tokenPath,
		}),
	}
	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error in Configure: %v", resp.Diagnostics)
	}

	cli, ok := resp.ResourceData.(*client.Client)
	if !ok || cli == nil {
		t.Fatalf("expected ResourceData to be *client.Client, got %T", resp.ResourceData)
	}
}

// TestProvider_SecretAttributes_AreSensitive verifies display-redaction metadata
// across the provider schema and all ephemeral resources. Sensitive alone does
// not prevent persistence; black-box tests separately prove ephemeral omission.
func TestProvider_SecretAttributes_AreSensitive(t *testing.T) {
	ctx := context.Background()
	p := nsProvider.New("v1.0.0")()

	// 1. Provider schema
	pSchemaResp := &provider.SchemaResponse{}
	p.Schema(ctx, provider.SchemaRequest{}, pSchemaResp)
	if tokAttr, ok := pSchemaResp.Schema.Attributes["token"]; !ok || !tokAttr.IsSensitive() {
		t.Errorf("provider 'token' attribute must be marked Sensitive")
	}

	// 2. Ephemeral resources schemas
	pWithEph, ok := p.(provider.ProviderWithEphemeralResources)
	if !ok {
		t.Fatalf("provider does not implement ProviderWithEphemeralResources")
	}

	expectedSensitives := map[string][]string{
		"namespace_tenant_token":       {"bearer_token"},
		"namespace_id_token":           {"id_token"},
		"namespace_client_certificate": {"client_certificate_pem", "private_key_pem"},
		"namespace_bazel_cache":        {"client_cert_pem", "client_key_pem"},
		"namespace_gradle_cache":       {"username", "password"},
		"namespace_http_cache":         {"username", "password"},
	}

	for _, ephFunc := range pWithEph.EphemeralResources(ctx) {
		eph := ephFunc()
		var metaResp ephemeral.MetadataResponse
		eph.Metadata(ctx, ephemeral.MetadataRequest{ProviderTypeName: "namespace"}, &metaResp)

		var schemaResp ephemeral.SchemaResponse
		eph.Schema(ctx, ephemeral.SchemaRequest{}, &schemaResp)

		expected, exists := expectedSensitives[metaResp.TypeName]
		if !exists {
			continue
		}

		for _, attrName := range expected {
			attr, hasAttr := schemaResp.Schema.Attributes[attrName]
			if !hasAttr {
				t.Errorf("resource %q missing expected attribute %q", metaResp.TypeName, attrName)
				continue
			}
			if !attr.IsSensitive() {
				t.Errorf("resource %q attribute %q must be marked Sensitive", metaResp.TypeName, attrName)
			}
		}
	}
}
