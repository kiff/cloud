package receipt_test

import "github.com/kiff/cloud/api/internal/receipt"

// refundDomainCompliance is a compliance map for the tests' refund
// domain: what a receipt over it claims to evidence.
func refundDomainCompliance() []receipt.ComplianceMapping {
	return []receipt.ComplianceMapping{
		{Framework: "SOC2", ControlID: "CC7.2", EvidenceKind: "system_monitoring"},
		{Framework: "EU_AI_ACT", ControlID: "Article 12", EvidenceKind: "record_keeping"},
		{Framework: "ISO_42001", ControlID: "8.3", EvidenceKind: "operational_controls"},
	}
}
