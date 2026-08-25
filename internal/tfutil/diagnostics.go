package tfutil

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AddError appends an error diagnostic to the diagnostics slice.
func AddError(diags *diag.Diagnostics, summary, detail string) {
	if diags == nil {
		return
	}
	diags.AddError(summary, detail)
}

// AddErrorf appends a formatted error diagnostic to the diagnostics slice.
func AddErrorf(diags *diag.Diagnostics, summary, format string, args ...any) {
	if diags == nil {
		return
	}
	diags.AddError(summary, fmt.Sprintf(format, args...))
}

// AddErrorDiag appends an error diagnostic if err is not nil.
func AddErrorDiag(diags *diag.Diagnostics, summary string, err error) {
	if diags == nil || err == nil {
		return
	}
	diags.AddError(summary, err.Error())
}

// AddWarning appends a warning diagnostic to the diagnostics slice.
func AddWarning(diags *diag.Diagnostics, summary, detail string) {
	if diags == nil {
		return
	}
	diags.AddWarning(summary, detail)
}

// AddWarningf appends a formatted warning diagnostic to the diagnostics slice.
func AddWarningf(diags *diag.Diagnostics, summary, format string, args ...any) {
	if diags == nil {
		return
	}
	diags.AddWarning(summary, fmt.Sprintf(format, args...))
}

// AddAttributeError appends an attribute-scoped error diagnostic.
func AddAttributeError(diags *diag.Diagnostics, p path.Path, summary, detail string) {
	if diags == nil {
		return
	}
	diags.AddAttributeError(p, summary, detail)
}

// AddAttributeErrorf appends a formatted attribute-scoped error diagnostic.
func AddAttributeErrorf(diags *diag.Diagnostics, p path.Path, summary, format string, args ...any) {
	if diags == nil {
		return
	}
	diags.AddAttributeError(p, summary, fmt.Sprintf(format, args...))
}

// AddAttributeWarning appends an attribute-scoped warning diagnostic.
func AddAttributeWarning(diags *diag.Diagnostics, p path.Path, summary, detail string) {
	if diags == nil {
		return
	}
	diags.AddAttributeWarning(p, summary, detail)
}

// AddAttributeWarningf appends a formatted attribute-scoped warning diagnostic.
func AddAttributeWarningf(diags *diag.Diagnostics, p path.Path, summary, format string, args ...any) {
	if diags == nil {
		return
	}
	diags.AddAttributeWarning(p, summary, fmt.Sprintf(format, args...))
}

// HasError returns true if the diagnostics contain at least one error.
func HasError(diags diag.Diagnostics) bool {
	return diags.HasError()
}

// DiagnosticsToError converts error diagnostics into a single Go error.
// If there are no error diagnostics, it returns nil.
func DiagnosticsToError(diags diag.Diagnostics) error {
	var errMsgs []string
	for _, d := range diags.Errors() {
		if d.Detail() != "" {
			errMsgs = append(errMsgs, fmt.Sprintf("%s: %s", d.Summary(), d.Detail()))
		} else {
			errMsgs = append(errMsgs, d.Summary())
		}
	}
	if len(errMsgs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errMsgs, "; "))
}

// HandleGRPCError checks if a gRPC error is a NotFound code.
// If err is nil, it returns false.
// If the error status code is codes.NotFound, it returns true without adding an error diagnostic.
// For all other errors, it appends an error diagnostic to diags and returns false.
func HandleGRPCError(diags *diag.Diagnostics, err error, action, target string) (notFound bool) {
	if err == nil {
		return false
	}

	st, ok := status.FromError(err)
	if ok && st.Code() == codes.NotFound {
		return true
	}

	if diags != nil {
		summary := fmt.Sprintf("Failed to %s %s", action, target)
		detail := err.Error()
		if ok {
			detail = fmt.Sprintf("gRPC error [%s]: %s", st.Code(), st.Message())
		}
		diags.AddError(summary, detail)
	}

	return false
}
