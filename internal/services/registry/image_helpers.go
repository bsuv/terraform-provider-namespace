package registry

import (
	"context"

	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func flattenImageLabels(ctx context.Context, labels []*stdlib.Label) (types.Map, diag.Diagnostics) {
	labelsMap := make(map[string]string, len(labels))
	for _, lbl := range labels {
		if lbl == nil || lbl.GetName() == "" {
			continue
		}
		labelsMap[lbl.GetName()] = lbl.GetValue()
	}
	return types.MapValueFrom(ctx, types.StringType, labelsMap)
}
