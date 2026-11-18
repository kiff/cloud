package words

// European Portuguese Activity lines (formal). The call is feminine.
func init() {
	why := func(w string) string {
		if w == "" {
			return ""
		}
		return ": " + w
	}
	pt.Activity = Activity{
		ByYou: "por si", BySomeone: "por uma pessoa da conta",
		ByRole:      map[string]string{"admin": "por um administrador", "editor": "por um editor", "approver": "por um aprovador", "owner": "pelo proprietário"},
		ByOtherRole: func(string) string { return "por uma pessoa da conta" },

		WaitingForYou:  "À sua espera",
		NotSent:        "Não enviado",
		NotSentBecause: func(reason string) string { return "Não enviado · " + reason },
		ApprovedOnceBy: func(by string) string { return "Aprovada uma vez " + by },
		ExtraRoomBy:    func(by string) string { return "Margem extra dada " + by },
		RefusedBy:      func(by string) string { return "Recusada " + by },

		AllowedByCard:    "Permitida pela sua Card",
		ApprovedBy:       func(by string) string { return "Aprovada " + by },
		ApprovedThenDone: func(by string) string { return "Aprovada " + by + " e depois concluída" },
		DoneWithinCard:   "Concluído, dentro da sua Card",
		ConnectionRefused: func(w string) string {
			return "Não enviado: a ligação recusou-a antes do envio" + why(w) + ". Continua a contar para os limites de hoje até o KIFF poder confirmar que nada foi feito."
		},
		AppRefusedCounts: func(w string) string { return "A app recusou-a, e continua a contar para os limites de hoje" + why(w) },
		AppRefusedMoney:  "A app recusou-a. Conta para o total de hoje até o KIFF poder confirmar que não se moveu dinheiro.",
		AppRefusedOther:  "A app recusou-a. Conta para o total de hoje até o KIFF poder confirmar que nada foi feito.",
		NotSentToTool: func(allowed, w string) string {
			return allowed + ", mas não foi possível enviá-la: nada chegou à ferramenta" + why(w)
		},
		Sending: func(allowed string) string { return allowed + ", e enviada: à espera da resposta da ferramenta" },
		AnswerLost: func(allowed string) string {
			return allowed + ", e enviada, mas a resposta da ferramenta perdeu-se: verifique os registos da própria ferramenta"
		},
		FoundAtVendor: func(allowed string) string {
			return allowed + ", e enviada. O KIFF não sabia como terminou, por isso verificou os registos da própria ferramenta e encontrou-a lá: foi concluída"
		},
		NotRecorded: func(allowed string) string {
			return allowed + ", mas não ficou registado se a ferramenta o fez: verifique os registos da própria ferramenta"
		},
		ConnectionWhy: map[string]string{
			"halt_unknown":        "não conseguiu verificar se o agente estava bloqueado",
			"halted":              "o agente ou a conta está bloqueado",
			"halted_before":       "a chamada foi aprovada antes do último bloqueio",
			"not_granted":         "a ligação não inclui esta ação",
			"permit_invalid":      "a autorização do KIFF não correspondia à chamada",
			"permit_expired":      "a autorização do KIFF expirou antes de a chamada sair",
			"bad_arguments":       "não foi possível ler os argumentos da chamada",
			"record_unavailable":  "não conseguiu guardar um registo da chamada",
			"profile_not_applied": "o KIFF ainda não aplicou as definições aprovadas",
			"definition_invalid":  "a ação indica um campo que a app não aceita",
			"definition_unread":   "não conseguiu verificar a ação junto da app",
			"operation_changed":   "a app alterou esta ação desde que foi aprovada; a ligação não envia nada até voltar a ser ligada",
			"signed_out":          "a app terminou a sessão; a ligação não envia nada até voltar a iniciar sessão",
		},
	}
}
