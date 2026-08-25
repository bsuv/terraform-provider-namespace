package tfutil

import (
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ISODateFormat is the format used for calendar dates (YYYY-MM-DD).
const ISODateFormat = "2006-01-02"

// FormatTimestamp converts a protobuf Timestamp into an RFC3339Nano string formatted in UTC.
// If t is nil or not valid, it returns an empty string.
func FormatTimestamp(t *timestamppb.Timestamp) string {
	if t == nil || !t.IsValid() {
		return ""
	}
	return t.AsTime().UTC().Format(time.RFC3339Nano)
}

// ParseTimestamp converts an RFC3339 or RFC3339Nano string into a protobuf Timestamp.
// If s is empty, it returns nil, nil.
// If the string cannot be parsed, it returns an error.
func ParseTimestamp(s string) (*timestamppb.Timestamp, error) {
	if s == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, fmt.Errorf("invalid RFC3339 timestamp %q: %w", s, err)
	}
	pb := timestamppb.New(parsed.UTC())
	if err := pb.CheckValid(); err != nil {
		return nil, fmt.Errorf("timestamp out of valid range: %w", err)
	}
	return pb, nil
}

// TimestampToTFString converts a protobuf Timestamp into a Terraform types.String.
// If t is nil or not valid, it returns types.StringNull().
func TimestampToTFString(t *timestamppb.Timestamp) types.String {
	if t == nil || !t.IsValid() {
		return types.StringNull()
	}
	return types.StringValue(FormatTimestamp(t))
}

// TimestampToTFStringPreserve converts a protobuf Timestamp into types.String, but preserves
// the existing string representation if it parses to the exact same instant in time.
func TimestampToTFStringPreserve(t *timestamppb.Timestamp, existing types.String) types.String {
	if t == nil || !t.IsValid() {
		return types.StringNull()
	}
	if !existing.IsNull() && !existing.IsUnknown() {
		if parsed, err := time.Parse(time.RFC3339Nano, existing.ValueString()); err == nil {
			if parsed.Equal(t.AsTime()) {
				return existing
			}
		}
	}
	return types.StringValue(FormatTimestamp(t))
}

// TFStringToTimestamp converts a Terraform types.String into a protobuf Timestamp.
// If s is null or unknown, it returns nil, nil.
func TFStringToTimestamp(s types.String) (*timestamppb.Timestamp, error) {
	if s.IsNull() || s.IsUnknown() {
		return nil, nil
	}
	return ParseTimestamp(s.ValueString())
}

// FormatDuration converts a protobuf Duration into a Go duration string (e.g. "24h0m0s", "30m0s").
// If d is nil or not valid, it returns an empty string.
func FormatDuration(d *durationpb.Duration) string {
	if d == nil || !d.IsValid() {
		return ""
	}
	return d.AsDuration().String()
}

// ParseDuration converts a duration string (e.g. "4h", "30m", "24h0m0s") into a protobuf Duration.
// If s is empty, it returns nil, nil.
// If the string cannot be parsed as a Go duration, it returns an error.
func ParseDuration(s string) (*durationpb.Duration, error) {
	if s == "" {
		return nil, nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return nil, fmt.Errorf("invalid duration string %q: %w", s, err)
	}
	pb := durationpb.New(dur)
	if err := pb.CheckValid(); err != nil {
		return nil, fmt.Errorf("duration out of valid range: %w", err)
	}
	return pb, nil
}

// DurationToTFString converts a protobuf Duration into a Terraform types.String.
// If d is nil or not valid, it returns types.StringNull().
func DurationToTFString(d *durationpb.Duration) types.String {
	if d == nil || !d.IsValid() {
		return types.StringNull()
	}
	return types.StringValue(FormatDuration(d))
}

// DurationToTFStringPreserve converts a protobuf Duration into types.String, but preserves
// the existing configured duration string if it parses to the exact same duration.
func DurationToTFStringPreserve(d *durationpb.Duration, existing types.String) types.String {
	if d == nil || !d.IsValid() {
		return types.StringNull()
	}
	if !existing.IsNull() && !existing.IsUnknown() {
		if parsed, err := time.ParseDuration(existing.ValueString()); err == nil {
			if parsed == d.AsDuration() {
				return existing
			}
		}
	}
	return types.StringValue(FormatDuration(d))
}

// TFStringToDuration converts a Terraform types.String into a protobuf Duration.
// If s is null or unknown, it returns nil, nil.
func TFStringToDuration(s types.String) (*durationpb.Duration, error) {
	if s.IsNull() || s.IsUnknown() {
		return nil, nil
	}
	return ParseDuration(s.ValueString())
}

// FormatDate formats a time.Time into a calendar date string (YYYY-MM-DD).
// If t is the zero value, it returns an empty string.
func FormatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(ISODateFormat)
}

// ParseDate parses a calendar date string formatted as YYYY-MM-DD into a time.Time.
// If s is empty, it returns the zero time.Time and nil error.
func ParseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(ISODateFormat, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid ISO date %q (expected YYYY-MM-DD): %w", s, err)
	}
	return t.UTC(), nil
}

// DateToTFString converts a time.Time date into a Terraform types.String.
// If t is the zero value, it returns types.StringNull().
func DateToTFString(t time.Time) types.String {
	if t.IsZero() {
		return types.StringNull()
	}
	return types.StringValue(FormatDate(t))
}

// TFStringToDate converts a Terraform types.String into a time.Time date.
// If s is null or unknown, it returns the zero time.Time and nil error.
func TFStringToDate(s types.String) (time.Time, error) {
	if s.IsNull() || s.IsUnknown() {
		return time.Time{}, nil
	}
	return ParseDate(s.ValueString())
}
