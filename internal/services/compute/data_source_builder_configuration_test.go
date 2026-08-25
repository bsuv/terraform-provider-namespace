package compute_test

import (
	"context"
	"testing"

	builderv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/builder/v1beta"
	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/services/compute"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceBuilderConfiguration(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := compute.NewBuilderConfigurationDataSource()

	ctx := context.Background()
	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)

	var confResp datasource.ConfigureResponse
	if confD, ok := d.(datasource.DataSourceWithConfigure); ok {
		confD.Configure(ctx, datasource.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return d, schemaResp
}

func TestBuilderConfigurationDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := compute.NewBuilderConfigurationDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_builder_configuration" {
		t.Fatalf("expected type name 'namespace_builder_configuration', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	for _, attrName := range []string{
		"id", "platform", "builder_name", "buildkit_endpoint",
		"full_buildkit_endpoint", "server_ca_pem", "cache_size_mb", "shape",
	} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing expected attribute %q", attrName)
		}
	}

	builderNameAttr, ok := attrs["builder_name"]
	if !ok {
		t.Fatal("missing builder_name attribute")
	}
	strAttr, ok := builderNameAttr.(schema.StringAttribute)
	if !ok {
		t.Fatal("builder_name is not schema.StringAttribute")
	}
	if len(strAttr.Validators) == 0 {
		t.Error("expected validators on builder_name attribute")
	}
}

func TestBuilderConfigurationDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := compute.NewBuilderConfigurationDataSource()

	var confResp datasource.ConfigureResponse
	if confD, ok := d.(datasource.DataSourceWithConfigure); ok {
		confD.Configure(ctx, datasource.ConfigureRequest{
			ProviderData: "invalid-type",
		}, &confResp)
		if !confResp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for invalid configure provider data")
		}
	}
}

func TestBuilderConfigurationDataSource_Read_SecurityAndSuppression(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	serverCaPem, _ := testutil.GenerateTestCertificate(t, "builder.namespace.so")
	clientCertPem, clientKeyPem := testutil.GenerateTestCertificate(t, "client.namespace.so")

	var capturedReq *builderv1beta.GetBuilderConfigurationRequest
	srv.Builder.GetBuilderConfigurationFunc = func(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error) {
		capturedReq = req
		return &builderv1beta.GetBuilderConfigurationResponse{
			BuildkitEndpoint:     "builder.namespace.so:443",
			FullBuildkitEndpoint: "tcp://builder.namespace.so:443",
			ServerCaPem:          serverCaPem,
			CacheSizeMb:          51200,
			Shape: &computev1beta.InstanceShape{
				VirtualCpu:      8,
				MemoryMegabytes: 16384,
				MachineArch:     "amd64",
				Os:              "linux",
			},
			// Even if server erroneously returns credentials, the provider MUST discard them
			Credentials: &builderv1beta.BuilderCredentials{
				ClientCertificatePem: clientCertPem,
				PrivateKeyPem:        clientKeyPem,
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceBuilderConfiguration(t, srv)

	cfg := compute.BuilderConfigurationDataSourceModel{
		ID:                   types.StringNull(),
		Platform:             types.StringValue("linux/amd64"),
		BuilderName:          types.StringValue("my-builder"),
		BuildkitEndpoint:     types.StringNull(),
		FullBuildkitEndpoint: types.StringNull(),
		ServerCaPem:          types.StringNull(),
		CacheSizeMb:          types.Int64Null(),
		Shape:                nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read diagnostics: %v", resp.Diagnostics)
	}

	// 1. Assert Critical Security Request Parameters
	if capturedReq == nil {
		t.Fatal("expected GetBuilderConfigurationRequest to be captured")
	}
	if !capturedReq.GetSkipBuilderPreSpawn() {
		t.Errorf("SECURITY VIOLATION: SkipBuilderPreSpawn must be true, got %v", capturedReq.GetSkipBuilderPreSpawn())
	}
	if capturedReq.GetReturnNewCredentials() {
		t.Errorf("SECURITY VIOLATION: ReturnNewCredentials must be false, got %v", capturedReq.GetReturnNewCredentials())
	}
	if capturedReq.GetPlatform() != "linux/amd64" {
		t.Errorf("expected platform 'linux/amd64', got %q", capturedReq.GetPlatform())
	}
	if capturedReq.GetBuilderName() != "my-builder" {
		t.Errorf("expected builder_name 'my-builder', got %q", capturedReq.GetBuilderName())
	}

	// 2. Assert State Population
	var state compute.BuilderConfigurationDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.ID.ValueString() != "my-builder:linux/amd64" {
		t.Errorf("expected ID 'my-builder:linux/amd64', got %q", state.ID.ValueString())
	}
	if state.BuildkitEndpoint.ValueString() != "builder.namespace.so:443" {
		t.Errorf("expected BuildkitEndpoint 'builder.namespace.so:443', got %q", state.BuildkitEndpoint.ValueString())
	}
	if state.FullBuildkitEndpoint.ValueString() != "tcp://builder.namespace.so:443" {
		t.Errorf("expected FullBuildkitEndpoint 'tcp://builder.namespace.so:443', got %q", state.FullBuildkitEndpoint.ValueString())
	}
	if state.ServerCaPem.ValueString() != serverCaPem {
		t.Errorf("unexpected ServerCaPem: %q", state.ServerCaPem.ValueString())
	}
	if state.CacheSizeMb.ValueInt64() != 51200 {
		t.Errorf("expected CacheSizeMb 51200, got %d", state.CacheSizeMb.ValueInt64())
	}
	if state.Shape == nil {
		t.Fatal("expected Shape to be populated")
	}
	if state.Shape.VirtualCpu.ValueInt64() != 8 {
		t.Errorf("expected Shape.VirtualCpu 8, got %d", state.Shape.VirtualCpu.ValueInt64())
	}
	if state.Shape.MemoryMegabytes.ValueInt64() != 16384 {
		t.Errorf("expected Shape.MemoryMegabytes 16384, got %d", state.Shape.MemoryMegabytes.ValueInt64())
	}
	if state.Shape.MachineArch.ValueString() != "amd64" {
		t.Errorf("expected Shape.MachineArch 'amd64', got %q", state.Shape.MachineArch.ValueString())
	}
	if state.Shape.OS.ValueString() != "linux" {
		t.Errorf("expected Shape.OS 'linux', got %q", state.Shape.OS.ValueString())
	}
}

func TestBuilderConfigurationDataSource_Read_Error(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Builder.GetBuilderConfigurationFunc = func(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error) {
		return nil, status.Errorf(codes.PermissionDenied, "insufficient builder permissions")
	}

	d, schemaResp := setupTestDataSourceBuilderConfiguration(t, srv)

	cfg := compute.BuilderConfigurationDataSourceModel{
		ID:                   types.StringNull(),
		Platform:             types.StringNull(),
		BuilderName:          types.StringNull(),
		BuildkitEndpoint:     types.StringNull(),
		FullBuildkitEndpoint: types.StringNull(),
		ServerCaPem:          types.StringNull(),
		CacheSizeMb:          types.Int64Null(),
		Shape:                nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for permission denied")
	}
}
