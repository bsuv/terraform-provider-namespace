package tfutil

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Diagnostics Tests ---

func TestDiagnostics(t *testing.T) {
	t.Run("AddError and AddErrorf", func(t *testing.T) {
		var diags diag.Diagnostics
		AddError(&diags, "Summary 1", "Detail 1")
		AddErrorf(&diags, "Summary 2", "Detail %d", 2)

		if len(diags) != 2 {
			t.Fatalf("expected 2 diagnostics, got %d", len(diags))
		}
		if !HasError(diags) {
			t.Fatal("expected HasError to be true")
		}
		if diags[0].Summary() != "Summary 1" || diags[0].Detail() != "Detail 1" {
			t.Errorf("unexpected diag 0: %v", diags[0])
		}
		if diags[1].Summary() != "Summary 2" || diags[1].Detail() != "Detail 2" {
			t.Errorf("unexpected diag 1: %v", diags[1])
		}
	})

	t.Run("AddErrorDiag", func(t *testing.T) {
		var diags diag.Diagnostics
		AddErrorDiag(&diags, "Nil Error", nil)
		if len(diags) != 0 {
			t.Fatalf("expected 0 diagnostics for nil error, got %d", len(diags))
		}

		err := errors.New("something went wrong")
		AddErrorDiag(&diags, "Operation Failed", err)
		if len(diags) != 1 {
			t.Fatalf("expected 1 diagnostic, got %d", len(diags))
		}
		if diags[0].Summary() != "Operation Failed" || diags[0].Detail() != "something went wrong" {
			t.Errorf("unexpected diag: %v", diags[0])
		}
	})

	t.Run("AddWarning and AddWarningf", func(t *testing.T) {
		var diags diag.Diagnostics
		AddWarning(&diags, "Warn 1", "Warn Detail 1")
		AddWarningf(&diags, "Warn 2", "Warn Detail %d", 2)

		if len(diags) != 2 {
			t.Fatalf("expected 2 diagnostics, got %d", len(diags))
		}
		if HasError(diags) {
			t.Fatal("expected HasError to be false for warnings only")
		}
		if diags[0].Severity() != diag.SeverityWarning || diags[1].Severity() != diag.SeverityWarning {
			t.Error("expected severity warning")
		}
	})

	t.Run("AddAttributeError and AddAttributeErrorf", func(t *testing.T) {
		var diags diag.Diagnostics
		p := path.Root("test_attr")
		AddAttributeError(&diags, p, "Attr Err 1", "Detail 1")
		AddAttributeErrorf(&diags, p, "Attr Err 2", "Detail %d", 2)

		if len(diags) != 2 {
			t.Fatalf("expected 2 diagnostics, got %d", len(diags))
		}
		if !HasError(diags) {
			t.Fatal("expected HasError to be true")
		}
	})

	t.Run("AddAttributeWarning and AddAttributeWarningf", func(t *testing.T) {
		var diags diag.Diagnostics
		p := path.Root("test_attr")
		AddAttributeWarning(&diags, p, "Attr Warn 1", "Detail 1")
		AddAttributeWarningf(&diags, p, "Attr Warn 2", "Detail %d", 2)

		if len(diags) != 2 {
			t.Fatalf("expected 2 diagnostics, got %d", len(diags))
		}
		if HasError(diags) {
			t.Fatal("expected HasError to be false for warnings")
		}
	})

	t.Run("DiagnosticsToError", func(t *testing.T) {
		var emptyDiags diag.Diagnostics
		if err := DiagnosticsToError(emptyDiags); err != nil {
			t.Errorf("expected nil error for empty diags, got %v", err)
		}

		var warnDiags diag.Diagnostics
		warnDiags.AddWarning("W1", "D1")
		if err := DiagnosticsToError(warnDiags); err != nil {
			t.Errorf("expected nil error for warning diags, got %v", err)
		}

		var errDiags diag.Diagnostics
		errDiags.AddError("E1", "D1")
		errDiags.AddError("E2", "")
		err := DiagnosticsToError(errDiags)
		if err == nil {
			t.Fatal("expected non-nil error")
		}
		if !strings.Contains(err.Error(), "E1: D1") || !strings.Contains(err.Error(), "E2") {
			t.Errorf("unexpected error string: %v", err)
		}
	})

	t.Run("HandleGRPCError", func(t *testing.T) {
		// 1. Nil error
		var diags1 diag.Diagnostics
		notFound := HandleGRPCError(&diags1, nil, "read", "profile")
		if notFound || len(diags1) != 0 {
			t.Errorf("expected false and 0 diags for nil error, got %v, %d", notFound, len(diags1))
		}

		// 2. NotFound error
		var diags2 diag.Diagnostics
		nfErr := status.Error(codes.NotFound, "profile not found")
		notFound = HandleGRPCError(&diags2, nfErr, "read", "profile")
		if !notFound || len(diags2) != 0 {
			t.Errorf("expected true and 0 diags for NotFound error, got %v, %d", notFound, len(diags2))
		}

		// 3. Other gRPC error
		var diags3 diag.Diagnostics
		internalErr := status.Error(codes.Internal, "database timeout")
		notFound = HandleGRPCError(&diags3, internalErr, "create", "profile")
		if notFound || len(diags3) != 1 {
			t.Fatalf("expected false and 1 diag for Internal error, got %v, %d", notFound, len(diags3))
		}
		if !strings.Contains(diags3[0].Detail(), "database timeout") {
			t.Errorf("unexpected detail: %s", diags3[0].Detail())
		}

		// 4. Non-gRPC generic error
		var diags4 diag.Diagnostics
		genericErr := errors.New("network failure")
		notFound = HandleGRPCError(&diags4, genericErr, "delete", "profile")
		if notFound || len(diags4) != 1 {
			t.Fatalf("expected false and 1 diag for generic error, got %v, %d", notFound, len(diags4))
		}
	})
}

// --- Pagination Tests ---

func TestPagination(t *testing.T) {
	ctx := context.Background()

	t.Run("Single page pagination", func(t *testing.T) {
		fetcher := func(_ context.Context, pageToken string) ([]string, string, error) {
			if pageToken != "" {
				t.Fatalf("expected empty pageToken, got %q", pageToken)
			}
			return []string{"item1", "item2"}, "", nil
		}

		items, err := PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 2 || items[0] != "item1" || items[1] != "item2" {
			t.Errorf("unexpected items: %v", items)
		}
	})

	t.Run("Multi page pagination", func(t *testing.T) {
		pages := map[string]struct {
			items     []int
			nextToken string
		}{
			"":   {items: []int{1, 2}, nextToken: "p2"},
			"p2": {items: []int{3, 4}, nextToken: "p3"},
			"p3": {items: []int{5}, nextToken: ""},
		}

		fetcher := func(_ context.Context, token string) ([]int, string, error) {
			page, ok := pages[token]
			if !ok {
				return nil, "", errors.New("invalid token")
			}
			return page.items, page.nextToken, nil
		}

		items, err := PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 5 {
			t.Fatalf("expected 5 items, got %d: %v", len(items), items)
		}
		for i, v := range items {
			if v != i+1 {
				t.Errorf("expected item %d to be %d, got %d", i, i+1, v)
			}
		}
	})

	t.Run("Empty initial page", func(t *testing.T) {
		fetcher := func(_ context.Context, _ string) ([]string, string, error) {
			return nil, "", nil
		}

		items, err := PaginateAll(ctx, fetcher)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 0 {
			t.Errorf("expected 0 items, got %d", len(items))
		}
	})

	t.Run("Direct cycle detection (self cycle)", func(t *testing.T) {
		fetcher := func(_ context.Context, token string) ([]string, string, error) {
			return []string{"a"}, "p1", nil
		}

		_, err := PaginateAll(ctx, fetcher, WithInitialToken("p1"))
		if !errors.Is(err, ErrPaginationCycleDetected) {
			t.Fatalf("expected ErrPaginationCycleDetected, got %v", err)
		}
	})

	t.Run("Indirect cycle detection (p1 -> p2 -> p1)", func(t *testing.T) {
		fetcher := func(_ context.Context, token string) ([]string, string, error) {
			if token == "" {
				return []string{"a"}, "p1", nil
			}
			if token == "p1" {
				return []string{"b"}, "p2", nil
			}
			if token == "p2" {
				return []string{"c"}, "p1", nil
			}
			return nil, "", nil
		}

		_, err := PaginateAll(ctx, fetcher)
		if !errors.Is(err, ErrPaginationCycleDetected) {
			t.Fatalf("expected ErrPaginationCycleDetected, got %v", err)
		}
	})

	t.Run("Max pages limit exceeded", func(t *testing.T) {
		fetcher := func(_ context.Context, token string) ([]int, string, error) {
			return []int{1}, token + "_next", nil
		}

		_, err := PaginateAll(ctx, fetcher, WithMaxPages(5))
		if !errors.Is(err, ErrMaxPagesExceeded) {
			t.Fatalf("expected ErrMaxPagesExceeded, got %v", err)
		}
	})

	t.Run("Fetcher error propagation", func(t *testing.T) {
		expectedErr := errors.New("rpc failed")
		fetcher := func(_ context.Context, _ string) ([]int, string, error) {
			return nil, "", expectedErr
		}

		_, err := PaginateAll(ctx, fetcher)
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected error %v, got %v", expectedErr, err)
		}
	})

	t.Run("ForEachPage callback error propagation", func(t *testing.T) {
		fetcher := func(_ context.Context, token string) ([]int, string, error) {
			return []int{1, 2}, "p2", nil
		}
		expectedErr := errors.New("stop processing")
		err := ForEachPage(ctx, fetcher, func(items []int, _ string) error {
			return expectedErr
		})
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected %v, got %v", expectedErr, err)
		}
	})

	t.Run("Context cancellation", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()

		fetcher := func(_ context.Context, _ string) ([]int, string, error) {
			return []int{1}, "", nil
		}

		_, err := PaginateAll(cancelCtx, fetcher)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

// --- Time Tests ---

func TestTime(t *testing.T) {
	t.Run("FormatTimestamp", func(t *testing.T) {
		if s := FormatTimestamp(nil); s != "" {
			t.Errorf("expected empty string for nil, got %q", s)
		}

		utcTime := time.Date(2026, 8, 21, 20, 30, 0, 123456789, time.UTC)
		pb := timestamppb.New(utcTime)
		expected := "2026-08-21T20:30:00.123456789Z"
		if got := FormatTimestamp(pb); got != expected {
			t.Errorf("expected %q, got %q", expected, got)
		}
	})

	t.Run("ParseTimestamp", func(t *testing.T) {
		pb, err := ParseTimestamp("")
		if err != nil || pb != nil {
			t.Errorf("expected nil, nil for empty string, got %v, %v", pb, err)
		}

		s := "2026-08-21T20:30:00.123456789Z"
		pb, err = ParseTimestamp(s)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := FormatTimestamp(pb); got != s {
			t.Errorf("roundtrip failed: expected %q, got %q", s, got)
		}

		sNoNanos := "2026-08-21T20:30:00Z"
		pb, err = ParseTimestamp(sNoNanos)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := FormatTimestamp(pb); got != sNoNanos {
			t.Errorf("roundtrip failed: expected %q, got %q", sNoNanos, got)
		}

		_, err = ParseTimestamp("invalid-date")
		if err == nil {
			t.Fatal("expected error for invalid timestamp")
		}
	})

	t.Run("Timestamp types.String roundtrip", func(t *testing.T) {
		nullStr := TimestampToTFString(nil)
		if !nullStr.IsNull() {
			t.Errorf("expected null string for nil timestamp")
		}
		pb, err := TFStringToTimestamp(nullStr)
		if err != nil || pb != nil {
			t.Errorf("expected nil, nil for null string")
		}

		utcTime := time.Date(2026, 8, 21, 20, 30, 0, 0, time.UTC)
		pbOrig := timestamppb.New(utcTime)
		tfStr := TimestampToTFString(pbOrig)
		if tfStr.IsNull() || tfStr.ValueString() != "2026-08-21T20:30:00Z" {
			t.Errorf("unexpected tfStr: %v", tfStr)
		}

		pbParsed, err := TFStringToTimestamp(tfStr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pbParsed.AsTime() != utcTime {
			t.Errorf("expected %v, got %v", utcTime, pbParsed.AsTime())
		}
	})

	t.Run("FormatDuration", func(t *testing.T) {
		if s := FormatDuration(nil); s != "" {
			t.Errorf("expected empty string for nil duration, got %q", s)
		}

		pb := durationpb.New(4 * time.Hour)
		if got := FormatDuration(pb); got != "4h0m0s" {
			t.Errorf("expected 4h0m0s, got %q", got)
		}
	})

	t.Run("ParseDuration", func(t *testing.T) {
		pb, err := ParseDuration("")
		if err != nil || pb != nil {
			t.Errorf("expected nil, nil for empty duration, got %v, %v", pb, err)
		}

		testCases := []struct {
			input    string
			expected time.Duration
		}{
			{"4h", 4 * time.Hour},
			{"30m", 30 * time.Minute},
			{"1h30m", 90 * time.Minute},
			{"500ms", 500 * time.Millisecond},
		}

		for _, tc := range testCases {
			pb, err := ParseDuration(tc.input)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if pb.AsDuration() != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, pb.AsDuration())
			}
		}

		_, err = ParseDuration("not-a-duration")
		if err == nil {
			t.Fatal("expected error for invalid duration string")
		}
	})

	t.Run("Duration types.String roundtrip", func(t *testing.T) {
		nullStr := DurationToTFString(nil)
		if !nullStr.IsNull() {
			t.Errorf("expected null types.String")
		}
		pb, err := TFStringToDuration(nullStr)
		if err != nil || pb != nil {
			t.Errorf("expected nil, nil for null string")
		}

		pbOrig := durationpb.New(2 * time.Hour)
		tfStr := DurationToTFString(pbOrig)
		if tfStr.ValueString() != "2h0m0s" {
			t.Errorf("expected 2h0m0s, got %s", tfStr.ValueString())
		}
		pbParsed, err := TFStringToDuration(tfStr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pbParsed.AsDuration() != 2*time.Hour {
			t.Errorf("expected 2h, got %v", pbParsed.AsDuration())
		}
	})

	t.Run("Date helpers", func(t *testing.T) {
		if s := FormatDate(time.Time{}); s != "" {
			t.Errorf("expected empty string for zero time, got %q", s)
		}
		d, err := ParseDate("")
		if err != nil || !d.IsZero() {
			t.Errorf("expected zero time for empty date string")
		}

		testDate := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
		formatted := FormatDate(testDate)
		if formatted != "2026-08-21" {
			t.Errorf("expected 2026-08-21, got %q", formatted)
		}
		parsed, err := ParseDate(formatted)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parsed != testDate {
			t.Errorf("expected %v, got %v", testDate, parsed)
		}

		tfStr := DateToTFString(testDate)
		if tfStr.ValueString() != "2026-08-21" {
			t.Errorf("expected 2026-08-21, got %s", tfStr.ValueString())
		}
		parsedFromTF, err := TFStringToDate(tfStr)
		if err != nil || parsedFromTF != testDate {
			t.Errorf("expected %v, got %v (err: %v)", testDate, parsedFromTF, err)
		}

		_, err = ParseDate("2026/08/21")
		if err == nil {
			t.Fatal("expected error for invalid date format")
		}
	})
}

// --- Validator Tests ---

func TestValidators(t *testing.T) {
	ctx := context.Background()
	p := path.Root("test_field")

	t.Run("DurationAtLeast", func(t *testing.T) {
		val := DurationAtLeast(4 * time.Hour)
		if desc := val.Description(ctx); !strings.Contains(desc, "4h") {
			t.Errorf("unexpected description: %s", desc)
		}
		if md := val.MarkdownDescription(ctx); md != val.Description(ctx) {
			t.Errorf("markdown description should match description")
		}

		var respNull validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringNull()}, &respNull)
		if respNull.Diagnostics.HasError() {
			t.Errorf("null value should not error: %v", respNull.Diagnostics)
		}

		var respUnknown validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringUnknown()}, &respUnknown)
		if respUnknown.Diagnostics.HasError() {
			t.Errorf("unknown value should not error: %v", respUnknown.Diagnostics)
		}

		validCases := []string{"4h", "5h", "24h", "4h30m"}
		for _, c := range validCases {
			var resp validator.StringResponse
			val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue(c)}, &resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("expected duration %q to pass, got: %v", c, resp.Diagnostics)
			}
		}

		invalidCases := []string{"3h59m", "1h", "30m", "0s"}
		for _, c := range invalidCases {
			var resp validator.StringResponse
			val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue(c)}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Errorf("expected duration %q to fail validation", c)
			}
		}

		var respMalformed validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("invalid")}, &respMalformed)
		if !respMalformed.Diagnostics.HasError() {
			t.Error("expected malformed duration to fail validation")
		}
	})

	t.Run("DurationPositive", func(t *testing.T) {
		val := DurationPositive()

		var respPositive validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("1s")}, &respPositive)
		if respPositive.Diagnostics.HasError() {
			t.Errorf("expected 1s to pass: %v", respPositive.Diagnostics)
		}

		var respZero validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("0s")}, &respZero)
		if !respZero.Diagnostics.HasError() {
			t.Error("expected 0s to fail positive validator")
		}

		var respNegative validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("-5m")}, &respNegative)
		if !respNegative.Diagnostics.HasError() {
			t.Error("expected negative duration to fail positive validator")
		}
	})

	t.Run("DurationBetween", func(t *testing.T) {
		val := DurationBetween(1*time.Minute, 1*time.Hour)

		var respIn validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("30m")}, &respIn)
		if respIn.Diagnostics.HasError() {
			t.Errorf("expected 30m to pass: %v", respIn.Diagnostics)
		}

		var respLow validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("30s")}, &respLow)
		if !respLow.Diagnostics.HasError() {
			t.Error("expected 30s to fail lower bound")
		}

		var respHigh validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("2h")}, &respHigh)
		if !respHigh.Diagnostics.HasError() {
			t.Error("expected 2h to fail upper bound")
		}
	})

	t.Run("NonEmptyString", func(t *testing.T) {
		val := NonEmptyString()

		var respNull validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringNull()}, &respNull)
		if respNull.Diagnostics.HasError() {
			t.Error("null should pass")
		}

		var respValid validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("valid_name")}, &respValid)
		if respValid.Diagnostics.HasError() {
			t.Errorf("valid string failed: %v", respValid.Diagnostics)
		}

		var respEmpty validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("")}, &respEmpty)
		if !respEmpty.Diagnostics.HasError() {
			t.Error("empty string should fail")
		}

		var respWhitespace validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("   \t\n ")}, &respWhitespace)
		if !respWhitespace.Diagnostics.HasError() {
			t.Error("whitespace string should fail")
		}
	})

	t.Run("ValidEnum", func(t *testing.T) {
		val := ValidEnum("LINUX", "WINDOWS", "DARWIN")

		var respValid validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("LINUX")}, &respValid)
		if respValid.Diagnostics.HasError() {
			t.Errorf("valid enum failed: %v", respValid.Diagnostics)
		}

		var respInvalid validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("SOLARIS")}, &respInvalid)
		if !respInvalid.Diagnostics.HasError() {
			t.Error("invalid enum value should fail")
		}
		if !strings.Contains(respInvalid.Diagnostics[0].Detail(), "LINUX, WINDOWS, DARWIN") {
			t.Errorf("error message should list allowed values: %s", respInvalid.Diagnostics[0].Detail())
		}
	})

	t.Run("ValidEndpoint", func(t *testing.T) {
		val := ValidEndpoint()

		validEndpoints := []string{
			"https://global.namespaceapis.com:443",
			"https://iam.namespaceapis.com",
			"eu.compute.namespaceapis.com:443",
			"global.namespaceapis.com",
			"localhost:8080",
		}
		for _, ep := range validEndpoints {
			var resp validator.StringResponse
			val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue(ep)}, &resp)
			if resp.Diagnostics.HasError() {
				t.Errorf("endpoint %q failed: %v", ep, resp.Diagnostics)
			}
		}

		invalidEndpoints := []string{
			"",
			"   ",
			"ftp://invalid-protocol.com",
			"http://",
			"http://localhost:8080",
			"https://global.namespaceapis.com:443/v1",
			"https://iam.namespaceapis.com/api",
			"eu.compute.namespaceapis.com:443/path",
			"global.namespaceapis.com/some/path",
		}
		for _, ep := range invalidEndpoints {
			var resp validator.StringResponse
			val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue(ep)}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Errorf("expected invalid endpoint %q to fail", ep)
			}
		}
	})

	t.Run("ValidRFC3339", func(t *testing.T) {
		val := ValidRFC3339()

		var respValid validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("2026-08-21T20:30:00Z")}, &respValid)
		if respValid.Diagnostics.HasError() {
			t.Errorf("valid RFC3339 failed: %v", respValid.Diagnostics)
		}

		var respInvalid validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("2026/08/21")}, &respInvalid)
		if !respInvalid.Diagnostics.HasError() {
			t.Error("invalid RFC3339 should fail")
		}
	})

	t.Run("ValidISODate", func(t *testing.T) {
		val := ValidISODate()

		var respValid validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("2026-08-21")}, &respValid)
		if respValid.Diagnostics.HasError() {
			t.Errorf("valid ISO date failed: %v", respValid.Diagnostics)
		}

		var respInvalid validator.StringResponse
		val.ValidateString(ctx, validator.StringRequest{Path: p, ConfigValue: types.StringValue("21-08-2026")}, &respInvalid)
		if !respInvalid.Diagnostics.HasError() {
			t.Error("invalid ISO date should fail")
		}
	})

	t.Run("Int64 Validators", func(t *testing.T) {
		valAtLeast := Int64AtLeast(10)
		var resp1 validator.Int64Response
		valAtLeast.ValidateInt64(ctx, validator.Int64Request{Path: p, ConfigValue: types.Int64Value(15)}, &resp1)
		if resp1.Diagnostics.HasError() {
			t.Errorf("15 should be >= 10: %v", resp1.Diagnostics)
		}
		var resp2 validator.Int64Response
		valAtLeast.ValidateInt64(ctx, validator.Int64Request{Path: p, ConfigValue: types.Int64Value(5)}, &resp2)
		if !resp2.Diagnostics.HasError() {
			t.Error("5 should fail >= 10")
		}

		valPositive := Int64Positive()
		var resp3 validator.Int64Response
		valPositive.ValidateInt64(ctx, validator.Int64Request{Path: p, ConfigValue: types.Int64Value(1)}, &resp3)
		if resp3.Diagnostics.HasError() {
			t.Errorf("1 should be positive: %v", resp3.Diagnostics)
		}
		var resp4 validator.Int64Response
		valPositive.ValidateInt64(ctx, validator.Int64Request{Path: p, ConfigValue: types.Int64Value(0)}, &resp4)
		if !resp4.Diagnostics.HasError() {
			t.Error("0 should fail positive")
		}

		valBetween := Int64Between(1, 10)
		var resp5 validator.Int64Response
		valBetween.ValidateInt64(ctx, validator.Int64Request{Path: p, ConfigValue: types.Int64Value(5)}, &resp5)
		if resp5.Diagnostics.HasError() {
			t.Errorf("5 should be in [1, 10]: %v", resp5.Diagnostics)
		}
		var resp6 validator.Int64Response
		valBetween.ValidateInt64(ctx, validator.Int64Request{Path: p, ConfigValue: types.Int64Value(15)}, &resp6)
		if !resp6.Diagnostics.HasError() {
			t.Error("15 should fail [1, 10]")
		}
	})
}
