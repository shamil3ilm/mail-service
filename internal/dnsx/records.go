package dnsx

import "fmt"

// Record is one DNS entry the user must publish. Type is uppercase per
// the DNS spec labels users see in registrar UIs (TXT, MX). Name is
// fully qualified (no trailing dot — registrars vary).
type Record struct {
	Kind        string `json:"kind"`        // "spf" | "dkim" | "dmarc" | "mx"
	Type        string `json:"type"`        // "TXT" | "MX"
	Name        string `json:"name"`        // fully-qualified DNS name
	Value       string `json:"value"`       // full record value
	Priority    int    `json:"priority,omitempty"` // MX only
	Description string `json:"description"` // human-readable purpose
}

// PolicyConfig lets callers tweak the DMARC/SPF defaults. All fields have
// safe defaults when zero-valued.
type PolicyConfig struct {
	// MXTarget is what recipients' MX should point at. In cloud mode this is
	// the public hostname of our SMTP inbound; in local mode it's a stub.
	MXTarget string
	// DMARCReportTo is the mailbox to send aggregated DMARC reports to.
	DMARCReportTo string
	// DMARCPolicy is one of "none", "quarantine", "reject".
	// "quarantine" is the sane default once the domain is proven.
	DMARCPolicy string
}

// RecordsFor returns the SPF/DKIM/DMARC/MX records the user needs to publish
// for `domain` to be a valid sender + receiver on this mail-service instance.
func RecordsFor(domain string, key *DKIMKey, cfg PolicyConfig) []Record {
	if cfg.MXTarget == "" {
		cfg.MXTarget = "mail." + domain
	}
	if cfg.DMARCPolicy == "" {
		cfg.DMARCPolicy = "quarantine"
	}
	if cfg.DMARCReportTo == "" {
		cfg.DMARCReportTo = "dmarc@" + domain
	}

	return []Record{
		{
			Kind:        "spf",
			Type:        "TXT",
			Name:        domain,
			Value:       fmt.Sprintf("v=spf1 a mx ip4:0.0.0.0/0 -all"),
			Description: "SPF: authorise senders. Tighten ip4:0.0.0.0/0 to your server's real IP in cloud mode.",
		},
		{
			Kind:        "dkim",
			Type:        "TXT",
			Name:        fmt.Sprintf("%s._domainkey.%s", key.Selector, domain),
			Value:       fmt.Sprintf("v=DKIM1; k=rsa; p=%s", key.PublicB64),
			Description: "DKIM: cryptographic signature key. Long strings may need splitting into 255-char chunks in some UIs.",
		},
		{
			Kind:        "dmarc",
			Type:        "TXT",
			Name:        "_dmarc." + domain,
			Value:       fmt.Sprintf("v=DMARC1; p=%s; rua=mailto:%s; adkim=r; aspf=r", cfg.DMARCPolicy, cfg.DMARCReportTo),
			Description: "DMARC: policy + reporting. Start with p=none, promote to quarantine once clean.",
		},
		{
			Kind:        "mx",
			Type:        "MX",
			Name:        domain,
			Value:       cfg.MXTarget,
			Priority:    10,
			Description: "MX: where senders should deliver mail for @" + domain + ".",
		},
	}
}
