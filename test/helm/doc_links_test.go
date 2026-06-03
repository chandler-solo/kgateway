package helm

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docsLinkRe matches any reference to the kgateway docs site, with or without a
// scheme, e.g. "https://kgateway.dev/docs/envoy/latest/install/advanced/".
var docsLinkRe = regexp.MustCompile(`https?://kgateway\.dev/docs/[^\s)"'` + "`" + `]*`)

// canonicalDocsPrefix is the only doc-URL shape that resolves on the published
// docs site. Every page lives under /docs/envoy/<version>/..., and the docs
// link checker remaps "https://kgateway.dev/docs/" onto the built site, where
// only the version-namespaced paths exist. Links that omit the "envoy/" segment
// (e.g. "/docs/operations/install/...", "/docs/latest/install/advanced/...",
// "/docs/integrations/inference-extension/") have all shipped broken and broken
// the docs repo's link-checker CI. "latest" tracks the newest release, which is
// what the chart's values should point at.
const canonicalDocsPrefix = "https://kgateway.dev/docs/envoy/latest/"

// TestHelmValuesDocLinks guards against doc links in the Helm charts that the
// docs site cannot resolve. The Helm reference tables on kgateway.dev are
// generated from these values.yaml descriptions, so a malformed link here
// becomes a broken link in the published docs. Enforcing the canonical
// "/docs/envoy/latest/..." shape catches the bug at PR time instead of in the
// downstream docs link-checker run.
func TestHelmValuesDocLinks(t *testing.T) {
	valuesFiles := []string{
		filepath.Join("..", "..", "install", "helm", "kgateway", "values.yaml"),
		filepath.Join("..", "..", "install", "helm", "kgateway-crds", "values.yaml"),
	}

	for _, vf := range valuesFiles {
		t.Run(vf, func(t *testing.T) {
			f, err := os.Open(vf)
			require.NoError(t, err, "failed to open values file %s", vf)
			defer f.Close()

			scanner := bufio.NewScanner(f)
			// Descriptions can be long single lines; raise the line limit well
			// above bufio's 64KiB default.
			scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

			lineNum := 0
			for scanner.Scan() {
				lineNum++
				for _, link := range docsLinkRe.FindAllString(scanner.Text(), -1) {
					// Trim trailing sentence punctuation so the prefix check is
					// not thrown off by e.g. a link that ends a sentence.
					link = strings.TrimRight(link, ".,;:")
					assert.Truef(t, strings.HasPrefix(link, canonicalDocsPrefix),
						"%s:%d: doc link %q is not version-namespaced; use the canonical %q... form so it resolves on the published docs site",
						vf, lineNum, link, canonicalDocsPrefix)
				}
			}
			require.NoError(t, scanner.Err(), "failed to scan values file %s", vf)
		})
	}
}
