package client

import (
	"context"
	"fmt"
	"sort"
	"strings"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/bsuv/terraform-provider-namespace/internal/redact"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RedactSecrets removes sensitive data (tokens, keys, certificates, passwords) from error strings and diagnostics.
func RedactSecrets(msg string) string {
	return redact.Secrets(msg)
}

// IsNotFound checks if the error is a gRPC NotFound status.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.NotFound
}

// IsAlreadyExists checks if the error is a gRPC AlreadyExists status.
func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.AlreadyExists
}

// IsFailedPrecondition checks if the error is a gRPC FailedPrecondition status.
func IsFailedPrecondition(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.FailedPrecondition
}

// HandleGRPCError inspects a gRPC error and adds formatted, redacted diagnostics.
// Returns true if the error represents a NotFound condition.
func HandleGRPCError(ctx context.Context, err error, diags *diag.Diagnostics, action string, resourceName string) bool {
	if err == nil {
		return false
	}

	st, ok := status.FromError(err)
	if !ok {
		diags.AddError(
			fmt.Sprintf("Unexpected Error during %s %s", action, resourceName),
			RedactSecrets(err.Error()),
		)
		return false
	}

	if st.Code() == codes.NotFound {
		if strings.EqualFold(action, "read") || strings.EqualFold(action, "delete") {
			return true
		}
		diags.AddError(
			fmt.Sprintf("%s Not Found during %s", resourceName, action),
			RedactSecrets(st.Message()),
		)
		return false
	}

	var detailMsgs []string
	isRevisionConflict := false

	for _, detail := range st.Details() {
		switch d := detail.(type) {
		case *errdetails.ErrorInfo:
			var metaStr string
			if len(d.Metadata) > 0 {
				var metaParts []string
				for k, v := range d.Metadata {
					metaParts = append(metaParts, fmt.Sprintf("%s=%s", k, v))
				}
				sort.Strings(metaParts)
				metaStr = fmt.Sprintf(", Metadata: {%s}", strings.Join(metaParts, ", "))
			}
			detailMsgs = append(detailMsgs, fmt.Sprintf("Reason: %s, Domain: %s%s", d.Reason, d.Domain, metaStr))
			if strings.EqualFold(d.Reason, "REVISION_MISMATCH") || strings.EqualFold(d.Reason, "VERSION_MISMATCH") || strings.EqualFold(d.Reason, "CONFLICT") {
				isRevisionConflict = true
			}
		case *errdetails.PreconditionFailure:
			for _, v := range d.Violations {
				detailMsgs = append(detailMsgs, fmt.Sprintf("Violation %q: %s (%s)", v.Type, v.Description, v.Subject))
				if strings.Contains(strings.ToLower(v.Type), "version") || strings.Contains(strings.ToLower(v.Type), "revision") {
					isRevisionConflict = true
				}
			}
		case *errdetails.BadRequest:
			for _, fv := range d.FieldViolations {
				detailMsgs = append(detailMsgs, fmt.Sprintf("Field %q: %s", fv.Field, fv.Description))
			}
		case *iamv1beta.PermissionDeniedError:
			for _, p := range d.GetMissingAccessPermissions() {
				if p == nil {
					continue
				}
				if p.GetResourceId() != "" {
					detailMsgs = append(detailMsgs, fmt.Sprintf("Missing permission: resource_type=%q, resource_id=%q, action=%q", p.GetResourceType(), p.GetResourceId(), p.GetAction()))
				} else {
					detailMsgs = append(detailMsgs, fmt.Sprintf("Missing permission: resource_type=%q, action=%q", p.GetResourceType(), p.GetAction()))
				}
			}
		}
	}

	if strings.Contains(strings.ToLower(st.Message()), "version") || strings.Contains(strings.ToLower(st.Message()), "revision") {
		isRevisionConflict = true
	}

	detailsStr := ""
	if len(detailMsgs) > 0 {
		detailsStr = "\nDetails:\n- " + strings.Join(detailMsgs, "\n- ")
	}

	summary := fmt.Sprintf("gRPC Error (%s) during %s %s", st.Code(), action, resourceName)
	detail := fmt.Sprintf("%s%s", st.Message(), detailsStr)

	switch st.Code() {
	case codes.FailedPrecondition, codes.Aborted:
		if isRevisionConflict {
			summary = fmt.Sprintf("Concurrency Conflict on %s during %s", resourceName, action)
			detail = fmt.Sprintf("The resource was modified outside of Terraform: %s. Run `terraform refresh` and re-plan.%s", st.Message(), detailsStr)
		}
	case codes.AlreadyExists:
		summary = fmt.Sprintf("%s Already Exists during %s", resourceName, action)
		if resourceName == "namespace_registry_shared_image" {
			detail = fmt.Sprintf("A shared image reference with identical parameters already exists: %s. Shared images cannot be imported because source repository information is not returned by the API; choose a distinct suffix or unshare the existing image.%s", st.Message(), detailsStr)
		} else {
			detail = fmt.Sprintf("A resource with the same identifier already exists remotely: %s. Use `terraform import` to manage it.%s", st.Message(), detailsStr)
		}
	case codes.Unauthenticated, codes.PermissionDenied:
		summary = fmt.Sprintf("Authorization Failure during %s %s", action, resourceName)
		detail = fmt.Sprintf("Namespace API returned %s: %s. Verify provider credentials and IAM permissions.%s", st.Code(), st.Message(), detailsStr)
	}

	diags.AddError(summary, RedactSecrets(detail))
	return false
}
