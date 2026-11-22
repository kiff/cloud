package words

// A receipt's steps in pt. A draft for the languages' owner to correct.
func init() {
	pt.ReceiptSteps = ReceiptSteps{
		ReceiptStepLines: map[string]string{
			"decision.allowed":           "Permitida",
			"decision.refused":           "Recusada",
			"decision.held_for_owner":    "Retida para o seu proprietário",
			"decision.held_for_approval": "Retida para aprovação",
			"decision.approved_no_room":  "Aprovada, à espera de margem",
			"decision.invalid":           "Inválida",
			"settled.approved_once":      "Aprovada uma vez",
			"settled.rejected":           "Rejeitada",
			"settled.rejected_next_step": "Rejeitada. Próximo passo: %s",
			"settled.card_changed":       "Em vez disso, o Card foi alterado",
			"settled.fit_on_retry":       "Ninguém respondeu; numa nova tentativa cabia no Card tal como estava",
			"settled.expired":            "Ninguém respondeu antes de a retenção expirar",
			"settled.withdrawn":          "Retirada",
			"settled.withdrawn_reason":   "Retirada: %s",
			"tool.reported":              "A ferramenta indicou: %s",
			"handoff.handed":             "Entregue ao seu proprietário; o KIFF não envia nada neste passo",
			"handoff.done":               "O proprietário diz que o fez",
			"handoff.skipped":            "O proprietário escolheu não o fazer",
			"handoff.no_answer":          "Ninguém respondeu antes de expirar; nunca lido como feito",
			"handoff.checked":            "Verificado junto do fornecedor após a resposta do proprietário: %s",
			"owner.halted":               "Bloqueado pelo proprietário: o KIFF não decide nenhuma das suas chamadas até ser retomado",
			"owner.resumed":              "Retomado pelo proprietário: as novas chamadas voltam a ser decididas, nada do que foi pedido antes do bloqueio",
			"state.not_allowed":          "Não permitida no seu estado atual: %s",
		},
		ReceiptAskedAgain: func(line string) string { return "Pedida novamente: " + line },
	}
}
