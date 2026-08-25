package tfutil

import (
	"context"
	"fmt"
	"strings"
	"time"

	endpointutil "github.com/bsuv/terraform-provider-namespace/internal/endpoint"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// --- Duration Validators ---

type durationAtLeastValidator struct {
	min time.Duration
}

// DurationAtLeast returns a validator ensuring that a string attribute is a valid Go duration
// that is greater than or equal to min.
func DurationAtLeast(min time.Duration) validator.String {
	return &durationAtLeastValidator{min: min}
}

func (v *durationAtLeastValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be a valid Go duration string representing at least %s (e.g. 4h, 24h)", v.min)
}

func (v *durationAtLeastValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *durationAtLeastValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueString()
	dur, err := time.ParseDuration(val)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Duration Format",
			fmt.Sprintf("Attribute %s must be a valid Go duration string (e.g., '4h', '30m'): %s", req.Path, err),
		)
		return
	}

	if dur < v.min {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Duration Below Minimum",
			fmt.Sprintf("Attribute %s duration (%s) must be at least %s", req.Path, val, v.min),
		)
	}
}

type durationPositiveValidator struct{}

// DurationPositive returns a validator ensuring that a string attribute is a valid Go duration
// strictly greater than zero.
func DurationPositive() validator.String {
	return &durationPositiveValidator{}
}

func (v *durationPositiveValidator) Description(_ context.Context) string {
	return "value must be a valid positive Go duration string (e.g. 30m, 4h)"
}

func (v *durationPositiveValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *durationPositiveValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueString()
	dur, err := time.ParseDuration(val)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Duration Format",
			fmt.Sprintf("Attribute %s must be a valid Go duration string (e.g., '4h', '30m'): %s", req.Path, err),
		)
		return
	}

	if dur <= 0 {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Duration Must Be Positive",
			fmt.Sprintf("Attribute %s duration (%s) must be greater than zero", req.Path, val),
		)
	}
}

type durationBetweenValidator struct {
	min time.Duration
	max time.Duration
}

// DurationBetween returns a validator ensuring that a string attribute is a valid Go duration
// within [min, max].
func DurationBetween(min, max time.Duration) validator.String {
	return &durationBetweenValidator{min: min, max: max}
}

func (v *durationBetweenValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be a valid duration between %s and %s", v.min, v.max)
}

func (v *durationBetweenValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *durationBetweenValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueString()
	dur, err := time.ParseDuration(val)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Duration Format",
			fmt.Sprintf("Attribute %s must be a valid Go duration string: %s", req.Path, err),
		)
		return
	}

	if dur < v.min || dur > v.max {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Duration Out of Range",
			fmt.Sprintf("Attribute %s duration (%s) must be between %s and %s", req.Path, val, v.min, v.max),
		)
	}
}

// --- String Validators ---

type nonEmptyStringValidator struct{}

// NonEmptyString returns a validator ensuring that a string attribute is not empty or pure whitespace.
func NonEmptyString() validator.String {
	return &nonEmptyStringValidator{}
}

func (v *nonEmptyStringValidator) Description(_ context.Context) string {
	return "value must be a non-empty string and cannot contain only whitespace"
}

func (v *nonEmptyStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *nonEmptyStringValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueString()
	if strings.TrimSpace(val) == "" {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid String Value",
			fmt.Sprintf("Attribute %s must not be empty or whitespace-only", req.Path),
		)
	}
}

type validEnumValidator struct {
	allowed []string
}

// ValidEnum returns a validator ensuring that a string attribute matches one of the allowed values.
func ValidEnum(allowed ...string) validator.String {
	return &validEnumValidator{allowed: allowed}
}

func (v *validEnumValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be one of: %s", strings.Join(v.allowed, ", "))
}

func (v *validEnumValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *validEnumValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueString()
	for _, a := range v.allowed {
		if val == a {
			return
		}
	}

	resp.Diagnostics.AddAttributeError(
		req.Path,
		"Invalid Attribute Value",
		fmt.Sprintf("Attribute %s value %q is invalid. Must be one of [%s]", req.Path, val, strings.Join(v.allowed, ", ")),
	)
}

type validEndpointValidator struct{}

// ValidEndpoint returns a validator ensuring that a string attribute is a valid HTTPS endpoint URL or host[:port] (HTTP is rejected).
func ValidEndpoint() validator.String {
	return &validEndpointValidator{}
}

func (v *validEndpointValidator) Description(_ context.Context) string {
	return "value must be a valid HTTPS endpoint URL or host[:port]"
}

func (v *validEndpointValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *validEndpointValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := strings.TrimSpace(req.ConfigValue.ValueString())
	if err := endpointutil.Validate(val); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Endpoint",
			fmt.Sprintf("Attribute %s %s", req.Path, err),
		)
	}
}

type validRFC3339Validator struct{}

// ValidRFC3339 returns a validator ensuring that a string attribute is a valid RFC3339 or RFC3339Nano timestamp string.
func ValidRFC3339() validator.String {
	return &validRFC3339Validator{}
}

func (v *validRFC3339Validator) Description(_ context.Context) string {
	return "value must be a valid RFC3339 or RFC3339Nano timestamp string (e.g. '2026-08-21T20:30:00Z')"
}

func (v *validRFC3339Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *validRFC3339Validator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueString()
	_, err := time.Parse(time.RFC3339Nano, val)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid RFC3339 Timestamp",
			fmt.Sprintf("Attribute %s must be a valid RFC3339 timestamp (e.g. '2026-08-21T20:30:00Z'): %s", req.Path, err),
		)
	}
}

type validISODateValidator struct{}

// ValidISODate returns a validator ensuring that a string attribute is a calendar date formatted as YYYY-MM-DD.
func ValidISODate() validator.String {
	return &validISODateValidator{}
}

func (v *validISODateValidator) Description(_ context.Context) string {
	return "value must be a valid calendar date formatted as YYYY-MM-DD (e.g. '2026-08-21')"
}

func (v *validISODateValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *validISODateValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueString()
	_, err := time.Parse(ISODateFormat, val)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Date Format",
			fmt.Sprintf("Attribute %s must be formatted as YYYY-MM-DD (e.g. '2026-08-21'): %s", req.Path, err),
		)
	}
}

// --- Int64 Validators ---

type int64AtLeastValidator struct {
	min int64
}

// Int64AtLeast returns a validator ensuring that an int64 attribute is greater than or equal to min.
func Int64AtLeast(min int64) validator.Int64 {
	return &int64AtLeastValidator{min: min}
}

func (v *int64AtLeastValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be at least %d", v.min)
}

func (v *int64AtLeastValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *int64AtLeastValidator) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueInt64()
	if val < v.min {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Value Below Minimum",
			fmt.Sprintf("Attribute %s value (%d) must be at least %d", req.Path, val, v.min),
		)
	}
}

type int64PositiveValidator struct{}

// Int64Positive returns a validator ensuring that an int64 attribute is strictly greater than zero.
func Int64Positive() validator.Int64 {
	return &int64PositiveValidator{}
}

func (v *int64PositiveValidator) Description(_ context.Context) string {
	return "value must be a positive integer greater than zero"
}

func (v *int64PositiveValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *int64PositiveValidator) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueInt64()
	if val <= 0 {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Value Must Be Positive",
			fmt.Sprintf("Attribute %s value (%d) must be greater than zero", req.Path, val),
		)
	}
}

type int64BetweenValidator struct {
	min int64
	max int64
}

// Int64Between returns a validator ensuring that an int64 attribute is between min and max inclusive.
func Int64Between(min, max int64) validator.Int64 {
	return &int64BetweenValidator{min: min, max: max}
}

func (v *int64BetweenValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be between %d and %d", v.min, v.max)
}

func (v *int64BetweenValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v *int64BetweenValidator) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	val := req.ConfigValue.ValueInt64()
	if val < v.min || val > v.max {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Value Out of Range",
			fmt.Sprintf("Attribute %s value (%d) must be between %d and %d", req.Path, val, v.min, v.max),
		)
	}
}
