package provider_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

type mockPreCheckTB struct {
	mu       sync.Mutex
	failed   bool
	skipped  bool
	messages []string
}

func (m *mockPreCheckTB) Helper() {}
func (m *mockPreCheckTB) Skip(args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skipped = true
	m.messages = append(m.messages, fmt.Sprint(args...))
}
func (m *mockPreCheckTB) Fatal(args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = true
	m.messages = append(m.messages, fmt.Sprint(args...))
}
func (m *mockPreCheckTB) Fatalf(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = true
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}
func (m *mockPreCheckTB) Errorf(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = true
	m.messages = append(m.messages, fmt.Sprintf(format, args...))
}

func TestAdversarial_SetupHelpers_FailClosedOnInvalidTenant(t *testing.T) {
	t.Run("Precheck fails when TF_ACC is active but tenant is mismatched", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "safe-tenant")
		t.Setenv("NSC_TOKEN", "nsct_"+makeTestJWT("malicious-tenant"))

		m := &mockPreCheckTB{}
		testAccPreCheckImpl(m)

		if !m.failed {
			t.Fatal("expected precheck to fail closed on mismatched nsct_ token")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected mismatch message, got: %v", m.messages)
		}
	})

	t.Run("Partner precheck fails when TF_ACC is active but partner token is mismatched", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "safe-tenant")
		t.Setenv("NSC_PARTNER_TOKEN", "nsct_"+makeTestJWT("malicious-partner"))

		m := &mockPreCheckTB{}
		testAccPartnerPreCheckImpl(m)

		if !m.failed {
			t.Fatal("expected partner precheck to fail closed on mismatched token")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "does not match NSC_TEST_TENANT") {
			t.Fatalf("expected mismatch message, got: %v", m.messages)
		}
	})

	t.Run("Trusted authority precheck fails when token is opaque", func(t *testing.T) {
		t.Setenv("TF_ACC", "1")
		t.Setenv("NSC_TEST_TENANT", "safe-tenant")
		t.Setenv("NSC_TRUSTED_JWT", "opaque-trusted-token")

		m := &mockPreCheckTB{}
		testAccTrustedAuthorityPreCheckImpl(m)

		if !m.failed {
			t.Fatal("expected trusted authority precheck to fail closed on opaque token")
		}
		if len(m.messages) == 0 || !strings.Contains(m.messages[0], "unsupported or opaque token format") {
			t.Fatalf("expected unsupported format message, got: %v", m.messages)
		}
	})
}
