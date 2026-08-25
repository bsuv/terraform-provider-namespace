package client

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	privateKeyRegex          = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*PRIVATE KEY-----[\s\S]*?-----END (?:[A-Z0-9_-]+ )*PRIVATE KEY-----`)
	certRegex                = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9_-]+ )*CERTIFICATE-----[\s\S]*?-----END (?:[A-Z0-9_-]+ )*CERTIFICATE-----`)
	envVarRegex              = regexp.MustCompile(`(?i)\b(NSC_TOKEN|NSC_PARTNER_TOKEN|NSC_TRUSTED_JWT|NSC_TOKEN_FILE|CACHE_PASSWORD|GPG_PRIVATE_KEY|PASSPHRASE)=([^\s\n]+)`)
	authHeaderRegex          = regexp.MustCompile(`(?i)\b(authorization:\s*bearer\s+)(?:\[REDACTED TOKEN\]|[^\s,;{}]+)`)
	basicAuthorizationRegex  = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*basic\s+)(?:\[REDACTED SECRET\]|[^\r\n,;{}]+)`)
	quotedAuthorizationRegex = regexp.MustCompile(`(?i)((?:\\?["'])authorization(?:\\?["'])\s*[:=]\s*(?:\\?["']))([^"'\r\n]*?)((?:\\?["']))`)
	bearerValueRegex         = regexp.MustCompile(`(?i)^(bearer\s+)`)
	basicValueRegex          = regexp.MustCompile(`(?i)^(basic\s+)`)
	bearerTokenRegex         = regexp.MustCompile(`(?i)\b(bearer\s+)(?:\[REDACTED TOKEN\]|[a-zA-Z0-9_\-\.]{20,})`)
	nsTokenRegex             = regexp.MustCompile(`\b(?:nsct_|nscw_|nscu_|tok_|st_)[a-zA-Z0-9_\-\.]+`)
	jwtRegex                 = regexp.MustCompile(`\beyJ[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+`)
	quotedPassRegex          = regexp.MustCompile(`(?i)((?:\\?["'])?(?:password|passphrase)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED PASSWORD\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedPassRegex        = regexp.MustCompile(`(?i)\b((?:password|passphrase)[ \t]*[:=][ \t]*)(?:\[REDACTED PASSWORD\]|[^\s,;{}'"]+)`)
	quotedTokenFieldRegex    = regexp.MustCompile(`(?i)((?:\\?["'])?(?:token|bearer[_-]?token|id[_-]?token|session[_-]?token|access[_-]?token|refresh[_-]?token|partner[_-]?token|client[_-]?token|auth[_-]?token|trusted[_-]?jwt|jwt)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED (?:TOKEN|JWT)\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedTokenFieldRegex  = regexp.MustCompile(`(?i)\b((?:token|bearer[_-]?token|id[_-]?token|session[_-]?token|access[_-]?token|refresh[_-]?token|partner[_-]?token|client[_-]?token|auth[_-]?token|trusted[_-]?jwt|jwt)[ \t]*[:=][ \t]*)(?:\[REDACTED (?:TOKEN|JWT)\]|[^\s,;{}'"&]+)`)
	quotedKeyFieldRegex      = regexp.MustCompile(`(?i)((?:\\?["'])?(?:(?:api|client|private|signing)[_-]?key(?:[_-]?pem)?)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED PRIVATE KEY\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedKeyFieldRegex    = regexp.MustCompile(`(?i)\b((?:(?:api|client|private|signing)[_-]?key(?:[_-]?pem)?)[ \t]*[:=][ \t]*)(?:\[REDACTED PRIVATE KEY\]|[^\s,;{}'"&]+)`)
	quotedCertFieldRegex     = regexp.MustCompile(`(?i)((?:\\?["'])?(?:(?:client[_-]?)?cert(?:ificate)?(?:[_-]?pem)?)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED CERTIFICATE\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedCertFieldRegex   = regexp.MustCompile(`(?i)\b((?:(?:client[_-]?)?cert(?:ificate)?(?:[_-]?pem)?)[ \t]*[:=][ \t]*)(?:\[REDACTED CERTIFICATE\]|[^\s,;{}'"&]+)`)
	quotedSecretFieldRegex   = regexp.MustCompile(`(?i)((?:\\?["'])?(?:client[_-]?secret|credentials?|secret)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["']))(?:\[REDACTED SECRET\]|[^"'\r\n]*?)((?:\\?["']))`)
	unquotedSecretFieldRegex = regexp.MustCompile(`(?i)\b((?:client[_-]?secret|credentials?|secret)[ \t]*[:=][ \t]*)(?:\[REDACTED SECRET\]|[^\s,;{}'"&]+)`)
)

// RedactSecrets removes sensitive data (tokens, keys, certificates, passwords) from error strings and diagnostics.
func RedactSecrets(msg string) string {
	msg = privateKeyRegex.ReplaceAllString(msg, "[REDACTED PRIVATE KEY]")
	msg = certRegex.ReplaceAllString(msg, "[REDACTED CERTIFICATE]")
	msg = envVarRegex.ReplaceAllString(msg, "${1}=[REDACTED]")
	msg = authHeaderRegex.ReplaceAllString(msg, "${1}[REDACTED TOKEN]")
	msg = basicAuthorizationRegex.ReplaceAllString(msg, "${1}[REDACTED SECRET]")
	msg = quotedAuthorizationRegex.ReplaceAllStringFunc(msg, redactQuotedAuthorization)
	msg = bearerTokenRegex.ReplaceAllString(msg, "${1}[REDACTED TOKEN]")
	msg = nsTokenRegex.ReplaceAllString(msg, "[REDACTED TOKEN]")
	msg = jwtRegex.ReplaceAllString(msg, "[REDACTED JWT]")
	msg = quotedPassRegex.ReplaceAllString(msg, "${1}[REDACTED PASSWORD]${2}")
	msg = unquotedPassRegex.ReplaceAllString(msg, "${1}[REDACTED PASSWORD]")
	msg = redactTokenFields(msg, quotedTokenFieldRegex, "${1}[REDACTED TOKEN]${2}")
	msg = redactTokenFields(msg, unquotedTokenFieldRegex, "${1}[REDACTED TOKEN]")
	msg = quotedKeyFieldRegex.ReplaceAllString(msg, "${1}[REDACTED PRIVATE KEY]${2}")
	msg = unquotedKeyFieldRegex.ReplaceAllString(msg, "${1}[REDACTED PRIVATE KEY]")
	msg = quotedCertFieldRegex.ReplaceAllString(msg, "${1}[REDACTED CERTIFICATE]${2}")
	msg = unquotedCertFieldRegex.ReplaceAllString(msg, "${1}[REDACTED CERTIFICATE]")
	msg = quotedSecretFieldRegex.ReplaceAllString(msg, "${1}[REDACTED SECRET]${2}")
	msg = unquotedSecretFieldRegex.ReplaceAllString(msg, "${1}[REDACTED SECRET]")
	return msg
}

func redactQuotedAuthorization(match string) string {
	parts := quotedAuthorizationRegex.FindStringSubmatch(match)
	if len(parts) != 4 {
		return "[REDACTED TOKEN]"
	}

	if scheme := bearerValueRegex.FindStringSubmatch(parts[2]); len(scheme) == 2 {
		return parts[1] + scheme[1] + "[REDACTED TOKEN]" + parts[3]
	}
	if scheme := basicValueRegex.FindStringSubmatch(parts[2]); len(scheme) == 2 {
		return parts[1] + scheme[1] + "[REDACTED SECRET]" + parts[3]
	}

	return parts[1] + "[REDACTED TOKEN]" + parts[3]
}

func redactTokenFields(msg string, fieldRegex *regexp.Regexp, replacement string) string {
	return fieldRegex.ReplaceAllStringFunc(msg, func(match string) string {
		if strings.Contains(match, "[REDACTED JWT]") {
			return match
		}
		// A diagnostic such as "JWT: Bearer <token>" uses JWT as a label and
		// Bearer as an authentication scheme, not as the secret field value.
		// The bearer-token pass has already redacted the actual credential.
		if fieldRegex == unquotedTokenFieldRegex {
			parts := fieldRegex.FindStringSubmatch(match)
			if len(parts) > 1 && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(match, parts[1])), "bearer") {
				return match
			}
		}
		return fieldRegex.ReplaceAllString(match, replacement)
	})
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
			detail = fmt.Sprintf("The resource was modified outside of Terraform: %s. Run `terraform refresh` and re-plan.", st.Message())
		}
	case codes.AlreadyExists:
		summary = fmt.Sprintf("%s Already Exists during %s", resourceName, action)
		if resourceName == "namespace_registry_shared_image" {
			detail = fmt.Sprintf("A shared image reference with identical parameters already exists: %s. Shared images cannot be imported because source repository information is not returned by the API; choose a distinct suffix or unshare the existing image.", st.Message())
		} else {
			detail = fmt.Sprintf("A resource with the same identifier already exists remotely: %s. Use `terraform import` to manage it.", st.Message())
		}
	case codes.Unauthenticated, codes.PermissionDenied:
		summary = fmt.Sprintf("Authorization Failure during %s %s", action, resourceName)
		detail = fmt.Sprintf("Namespace API returned %s: %s. Verify provider credentials and IAM permissions.", st.Code(), st.Message())
	}

	diags.AddError(summary, RedactSecrets(detail))
	return false
}
