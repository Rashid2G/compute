package tailscale

import (
	"strings"
)

// RecommendedPolicy returns the HuJSON snippet an operator should review and
// add to the tailnet policy file so that Compute works out of the box.
//
// The integration NEVER applies this automatically: the operator reviews it
// and pastes it into https://console.tailscale.com/admin/acls. The snippet is
// strictly scoped to tag:compute-* identities and cannot grant anything to
// existing user devices or services.
func RecommendedPolicy(sandboxTag, nodeTag, masterTag string) string {
	if sandboxTag == "" {
		sandboxTag = TagSandbox
	}
	if nodeTag == "" {
		nodeTag = TagNode
	}
	if masterTag == "" {
		masterTag = TagMaster
	}
	var b strings.Builder
	b.WriteString("// --- Compute (on-demand-vms) Tailscale integration ---\n")
	b.WriteString("// Review, then merge into the tailnet policy file\n")
	b.WriteString("// (https://console.tailscale.com/admin/acls). Compute never applies this itself.\n\n")
	b.WriteString("\t// Compute owns these tags. Only admins may hand them out.\n")
	b.WriteString("\t\"tagOwners\": {\n")
	b.WriteString("\t\t\"" + masterTag + "\": [\"autogroup:admin\"],\n")
	b.WriteString("\t\t\"" + nodeTag + "\": [\"autogroup:admin\"],\n")
	b.WriteString("\t\t\"" + sandboxTag + "\": [\"autogroup:admin\"],\n")
	b.WriteString("\t},\n\n")
	b.WriteString("\t// Sandboxes publish their own services (custom MagicDNS domains)\n")
	b.WriteString("\t// and are auto-approved without admin involvement.\n")
	b.WriteString("\t\"autoApprovers\": {\n")
	b.WriteString("\t\t\"services\": {\n")
	b.WriteString("\t\t\t\"" + sandboxTag + "\": [\"" + sandboxTag + "\"],\n")
	b.WriteString("\t\t\t\"" + nodeTag + "\": [\"" + sandboxTag + "\"]\n")
	b.WriteString("\t\t},\n")
	b.WriteString("\t},\n\n")
	b.WriteString("\t// Who may reach compute devices. Restrict \"src\" to specific users\n")
	b.WriteString("\t// or groups instead of autogroup:member if you prefer. If your\n")
	b.WriteString("\t// clients are tagged (for example tag:t3-vm), add a second grant\n")
	b.WriteString("\t// with that tag as \"src\" and the compute tags as \"dst\".\n")
	b.WriteString("\t{\n")
	b.WriteString("\t\t\"src\": [\"autogroup:member\"],\n")
	b.WriteString("\t\t\"dst\": [\"" + masterTag + "\", \"" + nodeTag + "\", \"" + sandboxTag + "\"],\n")
	b.WriteString("\t\t\"ip\": [\"*\"]\n")
	b.WriteString("\t},\n")
	return b.String()
}
