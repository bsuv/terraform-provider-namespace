package tfutil

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
)

// DeferDataSourceReadIfUnknown stops a data-source read before any API call
// when one or more configuration attributes are unknown.
func DeferDataSourceReadIfUnknown(req datasource.ReadRequest, resp *datasource.ReadResponse, attributes ...string) bool {
	if len(attributes) == 0 {
		return false
	}

	if req.ClientCapabilities.DeferralAllowed {
		resp.Deferred = &datasource.Deferred{Reason: datasource.DeferredReasonDataSourceConfigUnknown}
		return true
	}

	quoted := make([]string, 0, len(attributes))
	for _, attribute := range attributes {
		quoted = append(quoted, fmt.Sprintf("`%s`", attribute))
	}
	resp.Diagnostics.AddError(
		"Unknown Data Source Configuration",
		"Namespace cannot read this data source until these attributes are known: "+strings.Join(quoted, ", ")+".",
	)
	return true
}
