package vault

import (
	"context"
	"fmt"

	vaultv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/vault/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &secretMetadataDataSource{}
	_ datasource.DataSourceWithConfigure = &secretMetadataDataSource{}
)

// NewSecretMetadataDataSource creates a new Vault secret metadata data source.
func NewSecretMetadataDataSource() datasource.DataSource {
	return &secretMetadataDataSource{}
}

type secretMetadataDataSource struct {
	client ServiceClient
}

// SecretMetadataDataSourceModel defines the data source state model.
type SecretMetadataDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	ObjectID    types.String `tfsdk:"object_id"`
	Description types.String `tfsdk:"description"`
	Version     types.String `tfsdk:"version"`
	CreatedAt   types.String `tfsdk:"created_at"`
	DeletedAt   types.String `tfsdk:"deleted_at"`
	Labels      types.Map    `tfsdk:"labels"`
}

func (d *secretMetadataDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_vault_secret_metadata"
}

func (d *secretMetadataDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads metadata (version, creation timestamp, labels) for a Namespace Vault secret without exposing its value.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Identifier for the secret metadata in Terraform state, matching `object_id`.",
				Computed:    true,
			},
			"object_id": schema.StringAttribute{
				Description: "Unique object identifier of the Vault secret to inspect.",
				Required:    true,
			},
			"description": schema.StringAttribute{
				Description: "Human-readable description of the secret object.",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "Opaque version identifier for optimistic concurrency.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "RFC3339 creation timestamp.",
				Computed:    true,
			},
			"deleted_at": schema.StringAttribute{
				Description: "RFC3339 deletion timestamp, if the secret has been soft-deleted.",
				Computed:    true,
			},
			"labels": schema.MapAttribute{
				Description: "Immutable key-value labels attached to the secret object.",
				ElementType: types.StringType,
				Computed:    true,
			},
		},
	}
}

func (d *secretMetadataDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cli, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	d.client = NewDomainClient(cli)
}

func (d *secretMetadataDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config SecretMetadataDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	var unknownAttributes []string
	if config.ObjectID.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "object_id")
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	objectID := config.ObjectID.ValueString()
	if objectID == "" {
		resp.Diagnostics.AddError("Missing Required Attribute", "`object_id` must be specified for namespace_vault_secret_metadata.")
		return
	}

	// CRITICAL SECURITY MANDATE:
	// Call ListObjects only. NEVER call DescribeObject.
	rpcResp, err := d.client.ListObjects(ctx, &vaultv1beta.ListObjectsRequest{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_vault_secret_metadata")
		return
	}

	var matches []*vaultv1beta.VaultObjectMetadata
	for _, obj := range rpcResp.GetObjects() {
		if obj.GetObjectId() == objectID && obj.GetDeletedAt() == nil {
			matches = append(matches, obj)
		}
	}

	if len(matches) == 0 {
		resp.Diagnostics.AddError(
			"Vault Secret Metadata Not Found",
			fmt.Sprintf("No secret metadata found with object_id %q", objectID),
		)
		return
	}
	if len(matches) > 1 {
		resp.Diagnostics.AddError(
			"Multiple Vault Secret Objects Found",
			fmt.Sprintf("Multiple secret objects found with object_id %q", objectID),
		)
		return
	}

	match := matches[0]

	var state SecretMetadataDataSourceModel
	state.ID = types.StringValue(match.GetObjectId())
	state.ObjectID = types.StringValue(match.GetObjectId())

	if match.GetDescription() != "" {
		state.Description = types.StringValue(match.GetDescription())
	} else {
		state.Description = types.StringNull()
	}

	if match.GetVersion() != "" {
		state.Version = types.StringValue(match.GetVersion())
	} else {
		state.Version = types.StringNull()
	}

	state.CreatedAt = tfutil.TimestampToTFString(match.GetCreatedAt())
	state.DeletedAt = tfutil.TimestampToTFString(match.GetDeletedAt())

	labelsMap := make(map[string]string)
	for _, lbl := range match.GetLabels() {
		labelsMap[lbl.GetName()] = lbl.GetValue()
	}

	mapVal, mapDiags := types.MapValueFrom(ctx, types.StringType, labelsMap)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Labels = mapVal

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
