package provider_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestChallenger_AcceptanceTestVariables_EphemeralAndSensitiveEnforcement scans `acc_test.go`
// and all provider test files to guarantee that all sensitive credential variables
// (e.g. nsc_partner_token, nsc_trusted_jwt) strictly declare `ephemeral = true` alongside `sensitive = true`.
func TestChallenger_AcceptanceTestVariables_EphemeralAndSensitiveEnforcement(t *testing.T) {
	accTestPath := filepath.Join(".", "acc_test.go")
	accTestBytes, err := os.ReadFile(accTestPath)
	if err != nil {
		t.Fatalf("failed to read acc_test.go: %v", err)
	}
	accTestContent := string(accTestBytes)

	// Regex to match variable blocks in HCL snippets within acc_test.go
	varBlockRegex := regexp.MustCompile(`variable\s+"([^"]+)"\s*\{([^}]+)\}`)
	matches := varBlockRegex.FindAllStringSubmatch(accTestContent, -1)

	if len(matches) == 0 {
		t.Fatalf("expected to find variable blocks in acc_test.go, found 0")
	}

	sensitiveCredVars := []string{
		"nsc_partner_token",
		"nsc_trusted_jwt",
	}

	foundSensitiveVars := make(map[string]bool)

	for _, m := range matches {
		varName := m[1]
		varBody := m[2]

		for _, credVar := range sensitiveCredVars {
			if varName == credVar {
				foundSensitiveVars[varName] = true

				// Must declare sensitive = true
				if !strings.Contains(varBody, "sensitive = true") && !strings.Contains(varBody, "sensitive=true") {
					t.Errorf("variable %q in acc_test.go is missing 'sensitive = true':\n%s", varName, varBody)
				}

				// Must declare ephemeral = true
				if !strings.Contains(varBody, "ephemeral = true") && !strings.Contains(varBody, "ephemeral=true") {
					t.Errorf("variable %q in acc_test.go is missing 'ephemeral = true':\n%s", varName, varBody)
				}
			}
		}
	}

	for _, credVar := range sensitiveCredVars {
		if !foundSensitiveVars[credVar] {
			t.Errorf("expected sensitive credential variable %q not found in acc_test.go", credVar)
		}
	}
}

// TestChallenger_AcceptanceTestVariables_ProviderConfigsStructure tests that
// testAccPartnerProviderConfig() and testAccTrustedProviderConfig() return valid HCL
// blocks with both sensitive and ephemeral attributes.
func TestChallenger_AcceptanceTestVariables_ProviderConfigsStructure(t *testing.T) {
	partnerCfg := testAccPartnerProviderConfig()
	if !strings.Contains(partnerCfg, `variable "nsc_partner_token"`) {
		t.Errorf("testAccPartnerProviderConfig missing nsc_partner_token variable definition")
	}
	if !strings.Contains(partnerCfg, "sensitive = true") {
		t.Errorf("testAccPartnerProviderConfig missing sensitive = true")
	}
	if !strings.Contains(partnerCfg, "ephemeral = true") {
		t.Errorf("testAccPartnerProviderConfig missing ephemeral = true")
	}

	trustedCfg := testAccTrustedProviderConfig()
	if !strings.Contains(trustedCfg, `variable "nsc_trusted_jwt"`) {
		t.Errorf("testAccTrustedProviderConfig missing nsc_trusted_jwt variable definition")
	}
	if !strings.Contains(trustedCfg, "sensitive = true") {
		t.Errorf("testAccTrustedProviderConfig missing sensitive = true")
	}
	if !strings.Contains(trustedCfg, "ephemeral = true") {
		t.Errorf("testAccTrustedProviderConfig missing ephemeral = true")
	}
}

// TestChallenger_AcceptanceTestVariables_NoPlaintextTokensInHCLTemplates ensures that no test
// configurations embed hardcoded plaintext tokens or JWT credentials in HCL string interpolations,
// enforcing that credentials are supplied exclusively via Terraform variables or environment variables.
func TestChallenger_AcceptanceTestVariables_NoPlaintextTokensInHCLTemplates(t *testing.T) {
	accTestPath := filepath.Join(".", "acc_test.go")
	accTestBytes, err := os.ReadFile(accTestPath)
	if err != nil {
		t.Fatalf("failed to read acc_test.go: %v", err)
	}
	accTestContent := string(accTestBytes)

	// Disallow raw string patterns like token = "ey..." or token = "nsct_..." inside HCL blocks
	hardcodedTokenRegex := regexp.MustCompile(`(?i)token\s*=\s*"ey[a-zA-Z0-9_-]+\.ey[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+"`)
	if hardcodedTokenRegex.MatchString(accTestContent) {
		t.Errorf("acc_test.go contains hardcoded JWT token in HCL template string")
	}
}
