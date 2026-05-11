package waypoint

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/onsi/gomega"
	authpb "istio.io/api/security/v1"
	authcr "istio.io/client-go/pkg/apis/security/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type policyTestExpectation struct {
	allow  int
	deny   int
	audit  int
	custom int
}

var separateAndDeduplicatePoliciesTests = []struct {
	name     string
	policies []*authcr.AuthorizationPolicy
	expected policyTestExpectation
}{
	{
		name: "Single DENY policy",
		policies: []*authcr.AuthorizationPolicy{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deny-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_DENY,
				},
			},
		},
		expected: policyTestExpectation{
			allow:  0,
			deny:   1,
			audit:  0,
			custom: 0,
		},
	},
	{
		name: "Single ALLOW policy",
		policies: []*authcr.AuthorizationPolicy{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "allow-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_ALLOW,
				},
			},
		},
		expected: policyTestExpectation{
			allow:  1,
			deny:   0,
			audit:  0,
			custom: 0,
		},
	},
	{
		name: "Single AUDIT policy",
		policies: []*authcr.AuthorizationPolicy{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "audit-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_AUDIT,
				},
			},
		},
		expected: policyTestExpectation{
			allow:  0,
			deny:   0,
			audit:  1,
			custom: 0,
		},
	},
	{
		name: "Single CUSTOM policy",
		policies: []*authcr.AuthorizationPolicy{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_CUSTOM,
				},
			},
		},
		expected: policyTestExpectation{
			allow:  0,
			deny:   0,
			audit:  0,
			custom: 1,
		},
	},
	{
		name: "Duplicate DENY policies",
		policies: []*authcr.AuthorizationPolicy{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-deny-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_DENY,
				},
			},
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-deny-policy", // Same name = duplicate
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_DENY,
				},
			},
		},
		expected: policyTestExpectation{
			allow:  0,
			deny:   1, // Should only count once
			audit:  0,
			custom: 0,
		},
	},
	{
		name: "Duplicate ALLOW policies",
		policies: []*authcr.AuthorizationPolicy{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-allow-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_ALLOW,
				},
			},
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-allow-policy", // Same name = duplicate
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_ALLOW,
				},
			},
		},
		expected: policyTestExpectation{
			allow:  1, // Should only count once
			deny:   0,
			audit:  0,
			custom: 0,
		},
	},
	{
		name: "Different namespaces - not duplicates",
		policies: []*authcr.AuthorizationPolicy{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "same-name-policy",
					Namespace: "namespace-1",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_DENY,
				},
			},
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "same-name-policy", // Same name but different namespace
					Namespace: "namespace-2",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_DENY,
				},
			},
		},
		expected: policyTestExpectation{
			allow:  0,
			deny:   2, // Should count both
			audit:  0,
			custom: 0,
		},
	},
	{
		name: "Mixed policy types",
		policies: []*authcr.AuthorizationPolicy{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "allow-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_ALLOW,
				},
			},
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "deny-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_DENY,
				},
			},
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "audit-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_AUDIT,
				},
			},
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-policy",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_CUSTOM,
				},
			},
		},
		expected: policyTestExpectation{
			allow:  1,
			deny:   1,
			audit:  1,
			custom: 1,
		},
	},
	{
		name: "Multiple duplicates of different types",
		policies: []*authcr.AuthorizationPolicy{
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dup-allow",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_ALLOW,
				},
			},
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dup-allow", // Duplicate
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_ALLOW,
				},
			},
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dup-deny",
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_DENY,
				},
			},
			{
				TypeMeta: metav1.TypeMeta{
					Kind:       "AuthorizationPolicy",
					APIVersion: "security.istio.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dup-deny", // Duplicate
					Namespace: "test-ns",
				},
				Spec: authpb.AuthorizationPolicy{
					Action: authpb.AuthorizationPolicy_DENY,
				},
			},
		},
		expected: policyTestExpectation{
			allow:  1, // Should count unique policies
			deny:   1,
			audit:  0,
			custom: 0,
		},
	},
}

// istioServiceAccountRegex mirrors istio.io/istio/pilot/pkg/security/authz/model.serviceAccountRegex
// at the pinned (pre-CVE-2026-39350-fix) commit. Used by the test below to
// verify that quoteMetaServiceAccounts produces a regex matching only the
// literal SPIFFE identifier — the same behavior that istio's own QuoteMeta
// fix achieves at the emission layer.
func istioServiceAccountRegex(defaultNamespace, value string) string {
	ns, sa, ok := strings.Cut(value, "/")
	if !ok {
		ns = defaultNamespace
		sa = value
	}
	return fmt.Sprintf("spiffe://.+/ns/%s/(.+/|)sa/%s(/.+)?", ns, sa)
}

// TestQuoteMetaServiceAccountsCVE_2026_39350 verifies that pre-escaping
// Source.ServiceAccounts / NotServiceAccounts yields an Envoy SafeRegex
// that matches only the literal SPIFFE id. Without the mitigation, dots
// in the SA value would behave as regex wildcards.
func TestQuoteMetaServiceAccountsCVE_2026_39350(t *testing.T) {
	g := gomega.NewWithT(t)

	spec := &authpb.AuthorizationPolicy{
		Rules: []*authpb.Rule{{
			From: []*authpb.Rule_From{{
				Source: &authpb.Source{
					ServiceAccounts:    []string{"my-ns/my.sa", "my.ns/my.sa"},
					NotServiceAccounts: []string{"evil/sa.bad"},
				},
			}},
		}},
	}

	quoteMetaServiceAccounts(spec)

	src := spec.Rules[0].From[0].Source
	g.Expect(src.ServiceAccounts).To(gomega.Equal([]string{`my-ns/my\.sa`, `my\.ns/my\.sa`}))
	g.Expect(src.NotServiceAccounts).To(gomega.Equal([]string{`evil/sa\.bad`}))

	// Verify that feeding the escaped value through istio's unfixed
	// serviceAccountRegex template yields a regex anchored to the literal SA.
	cases := []struct {
		value        string
		shouldMatch  string
		shouldNotHit string
		defaultNS    string
	}{
		{
			value:        `my-ns/my\.sa`,
			defaultNS:    "policy-ns",
			shouldMatch:  "spiffe://cluster.local/ns/my-ns/sa/my.sa",
			shouldNotHit: "spiffe://cluster.local/ns/my-ns/sa/myXsa",
		},
		{
			value:        `my\.ns/my\.sa`,
			defaultNS:    "policy-ns",
			shouldMatch:  "spiffe://cluster.local/ns/my.ns/sa/my.sa",
			shouldNotHit: "spiffe://cluster.local/ns/myXns/sa/my.sa",
		},
	}
	for _, tc := range cases {
		regex := istioServiceAccountRegex(tc.defaultNS, tc.value)
		re := regexp.MustCompile("^" + regex + "$")
		g.Expect(re.MatchString(tc.shouldMatch)).To(gomega.BeTrue(),
			"escaped value %q should match literal SPIFFE id %q (regex=%q)", tc.value, tc.shouldMatch, regex)
		g.Expect(re.MatchString(tc.shouldNotHit)).To(gomega.BeFalse(),
			"escaped value %q must not match wildcard variant %q (regex=%q)", tc.value, tc.shouldNotHit, regex)
	}
}

func TestSeparateAndDeduplicatePolicies(t *testing.T) {
	for _, tc := range separateAndDeduplicatePoliciesTests {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)

			// Call the actual function
			result := separateAndDeduplicatePolicies(tc.policies)
			t.Logf("Result: allow=%d, deny=%d, audit=%d, custom=%d",
				len(result.Allow), len(result.Deny), len(result.Audit), len(result.Custom))

			// Verify the results using HaveLen instead of checking length with Equal
			g.Expect(result.Allow).To(gomega.HaveLen(tc.expected.allow), "wrong number of ALLOW policies")
			g.Expect(result.Deny).To(gomega.HaveLen(tc.expected.deny), "wrong number of DENY policies")
			g.Expect(result.Audit).To(gomega.HaveLen(tc.expected.audit), "wrong number of AUDIT policies")
			g.Expect(result.Custom).To(gomega.HaveLen(tc.expected.custom), "wrong number of CUSTOM policies")
		})
	}
}
