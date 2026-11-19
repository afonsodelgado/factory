package install

import (
	"os"
	"regexp"
	"strings"
)

var ssoOrganisation = regexp.MustCompile(`The '([^']+)' organization has enabled or enforced SAML SSO`)

func GitHint(output string) string {
	if !strings.Contains(output, "SAML SSO") {
		return ""
	}
	organisation := "your organisation"
	if m := ssoOrganisation.FindStringSubmatch(output); m != nil {
		organisation = m[1]
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	return "open github.com/settings/keys, then Configure SSO on factory " + host + " and authorise it for " + organisation
}
