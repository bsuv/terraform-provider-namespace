package tfutil_test

import (
	"testing"

	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
)

func TestDeferDataSourceReadIfUnknown(t *testing.T) {
	t.Run("known configuration continues", func(t *testing.T) {
		resp := &datasource.ReadResponse{}
		if tfutil.DeferDataSourceReadIfUnknown(datasource.ReadRequest{}, resp) {
			t.Fatal("known configuration unexpectedly stopped the read")
		}
		if resp.Deferred != nil || len(resp.Diagnostics) != 0 {
			t.Fatalf("known configuration modified response: %#v", resp)
		}
	})

	t.Run("supported client defers", func(t *testing.T) {
		resp := &datasource.ReadResponse{}
		req := datasource.ReadRequest{ClientCapabilities: datasource.ReadClientCapabilities{DeferralAllowed: true}}
		if !tfutil.DeferDataSourceReadIfUnknown(req, resp, "repository", "reference") {
			t.Fatal("unknown configuration did not stop the read")
		}
		if resp.Deferred == nil || resp.Deferred.Reason != datasource.DeferredReasonDataSourceConfigUnknown {
			t.Fatalf("unexpected deferral: %#v", resp.Deferred)
		}
		if resp.Diagnostics.HasError() {
			t.Fatalf("supported deferral returned errors: %v", resp.Diagnostics)
		}
	})

	t.Run("unsupported client fails closed", func(t *testing.T) {
		resp := &datasource.ReadResponse{}
		if !tfutil.DeferDataSourceReadIfUnknown(datasource.ReadRequest{}, resp, "repository") {
			t.Fatal("unknown configuration did not stop the read")
		}
		if !resp.Diagnostics.HasError() {
			t.Fatal("unknown configuration without deferral support must return an error")
		}
		if resp.Deferred != nil {
			t.Fatalf("unsupported client received an invalid deferral: %#v", resp.Deferred)
		}
	})
}
