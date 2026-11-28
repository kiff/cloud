package words

import "fmt"

// Portuguese (você), European, in the glossary's terms.
var pt = Words{
	DecimalComma: true,

	WindowNextApproval:     "A sua impressão digital ou rosto na próxima aprovação",
	WindowAgainUnderMinute: "A sua impressão digital ou rosto de novo em menos de um minuto",
	WindowNoneNextMinute:   "Sem impressão digital nem rosto durante o próximo minuto",
	WindowNoneMinutes: func(n int) string {
		return fmt.Sprintf("Sem impressão digital nem rosto durante os próximos %d minutos", n)
	},

	Today:     "hoje",
	Yesterday: "ontem",
	Months:    [12]string{"jan.", "fev.", "mar.", "abr.", "mai.", "jun.", "jul.", "ago.", "set.", "out.", "nov.", "dez."},

	HappenedYes:           "Sim, foi feito",
	HappenedYesDetail:     "Continua a contar na Card.",
	HappenedNo:            "Não, não foi feito",
	HappenedNoDetail:      "A Card recupera a sua margem. Nada é revertido no fornecedor.",
	HappenedNotSure:       "Não tenho a certeza, ajude-me a verificar",
	HappenedNotSureDetail: "O Coach consulta consigo o histórico do fornecedor. A pergunta fica consigo durante um dia.",
	HappenedNoAfter:       "A Card recuperou a sua margem. Nada foi revertido no fornecedor: verifique lá se o pagamento chegou a ser feito e, se foi, diga ao Coach.",
	WhyLost: func(vendor string, read bool) string {
		of := "do fornecedor"
		if vendor != "" {
			of = "de " + vendor
		}
		if read {
			return "Foi enviada, mas a sua resposta perdeu-se, e as leituras do KIFF no histórico " + of + " ainda não o esclareceram."
		}
		return "Foi enviada, mas a sua resposta perdeu-se, e no histórico " + of + " não é possível procurar esta chamada."
	},
	WhyRefused: func(vendor string, read bool) string {
		if vendor == "" {
			vendor = "o fornecedor"
		}
		if read {
			return Capitalize(vendor) + " recusou-a sem dizer se algo foi cobrado, e as leituras do KIFF no seu histórico ainda não o esclareceram."
		}
		return Capitalize(vendor) + " recusou-a sem dizer se algo foi cobrado, e no seu histórico não é possível procurar esta chamada."
	},
	WhyProbation: "Uma das primeiras chamadas de uma ligação que o KIFF ainda não experimentou com uma conta de teste: confirme o que aconteceu.",
	StatusRefused: func(vendor string) string {
		if vendor == "" {
			return "O fornecedor recusou-a. Verifique junto do fornecedor se algum dinheiro foi movimentado."
		}
		return Capitalize(vendor) + " recusou-a. Verifique em " + vendor + " se algum dinheiro foi movimentado."
	},
	StatusLost: func(vendor string) string {
		if vendor == "" {
			return "Foi enviada para o fornecedor, mas a resposta perdeu-se. Verifique junto do fornecedor se foi feita."
		}
		return "Foi enviada para " + vendor + ", mas a resposta perdeu-se. Verifique em " + vendor + " se foi feita."
	},
	StatusProbation: "Uma das primeiras chamadas por esta ligação: verifique se fez o que esperava.",
	CallAtVendor:    func(action, vendor string) string { return action + " em " + vendor },
	CallByTaf:       func(taf string) string { return "por " + taf },

	Reasons: map[string]string{
		"mandate_owner_rejected":       "Recusou-a.",
		"mandate_owner_expired":        "Ninguém respondeu a tempo.",
		"mandate_withdrawn":            "Foi retirada antes de alguém responder.",
		"mandate_per_action_exceeded":  "Ultrapassava o limite da Card para uma chamada.",
		"mandate_limit_reached":        "Ultrapassava um dos totais da Card.",
		"mandate_resource_not_on_card": "Incidia sobre algo que a Card não cobre.",
		"mandate_unreadable_resource":  "O KIFF não conseguiu ler sobre o que incidia.",
		"mandate_not_covered":          "Nenhuma Card cobre esta ferramenta.",
		"mandate_category_closed":      "Está fechado no seu Card até o abrir.",
		"mandate_expired":              "A sua Card expirou.",
		"no_mandate_issued":            "Nenhuma Card cobre esta ferramenta.",
		"amount_unit_mismatch":         "Era noutra moeda que não a da Card.",
		"amount_unit_unverified":       "O KIFF não conseguiu verificar a moeda.",
		"amount_unverified":            "O KIFF não conseguiu ler o valor.",
		"halted":                       "Tinha-a bloqueado.",
		"halt_unavailable":             "O KIFF não conseguiu verificar se estava bloqueada.",
	},
	OwnerAsks: map[string]string{
		OwnerAskSealedMoney:  "esta ferramenta movimenta dinheiro numa ligação cuja chave o KIFF guarda, por isso cada chamada aguarda aprovação",
		OwnerAskPurchase:     "isto compra algo, e o cartão pergunta-lhe sempre, ou está fora dos seus limites",
		OwnerAskSecondRefund: "esta cobrança já tem um reembolso, por isso um segundo aguarda aprovação",
		OwnerAskNoRequestID:  "o agente não indicou nenhum pedido de uma app ligada, por isso este reembolso aguarda aprovação",
		OwnerAskOther:        "cada chamada desta ferramenta aguarda aprovação",
		OwnerAskEachCall:     "cada chamada desta ferramenta aguarda aprovação",
		OwnerAskSealedFill:   "este passo preenche um início de sessão guardado no seu Chrome, por isso espera por si sempre, e só o seu telemóvel o pode libertar",
	},
	ResourceMissing: func(param string) string {
		return fmt.Sprintf("a chamada não indica nenhum %s, e esta Card só se aplica aos %s que lista", param, param)
	},
	ResourceNotOnCard: func(param, value string) string {
		return fmt.Sprintf("%s %q não é um dos que esta Card cobre", param, value)
	},
	OverPerCall: func(draw, limit string) string {
		return fmt.Sprintf("%s ultrapassa o limite de %s por chamada", draw, limit)
	},
	OverTotal: func(draw, limit, used string) string {
		return fmt.Sprintf("%s ultrapassaria o limite de %s: já foram usados %s", draw, limit, used)
	},

	// The held-call and Card-warning emails, and a warning's reason.
	HeldSubject:     "O seu agente aguarda a sua resposta",
	HeldNothingDone: "Nada foi feito.",
	HeldOpen:        "Abra-o no KIFF para responder (ser-lhe-á pedido que inicie sessão):",
	HeldOptions:     "Pode aprová-lo uma vez, alterar a Card para o permitir ou recusá-lo.",
	HeldIntro: func(agent, action, amount string) string {
		return agent + " quer executar " + action + amount + ", o que está fora da sua Card."
	},
	HeldSteps: func(steps string) string {
		return "A Card também lhe permite recusá-lo com um próximo passo para o agente: " + steps + "."
	},
	HeldExpires: func(when string) string {
		return "Se ninguém responder até " + when + ", é recusado e o agente tem de pedir novamente."
	},
	HeldNoApprove:   "Nada pode ser aprovado a partir deste e-mail.",
	HeldButtonsNote: "Cada botão abre esta ação no KIFF, onde inicia sessão e confirma a sua resposta.",
	HeldApproveOnce: "Aprovar uma vez",
	HeldChangeCard:  "Alterar a Card para o permitir",
	HeldReject:      "Recusar",
	HeldWants: func(agent string) string {
		return fmt.Sprintf("%s quer fazer algo que está fora do seu Card:", agent)
	},
	HeldRowAction: "Ação", HeldRowAmount: "Valor", HeldRowWhy: "Por que está retida",
	WarnSpent:  "Este total da Card está esgotado.",
	WarnBurst:  "Uma Card nova usou mais do que o seu limiar de pico no início deste período.",
	WarnRunway: "Ao ritmo recente, este total da Card está a esgotar-se.",
	WarnUnusual: func(ratio string) string {
		return "A utilização recente é " + ratio + " vezes a sua média equivalente de sete dias."
	},
	WarnUsed:         func(pct, period string) string { return pct + "% deste total está usado " + period + "." },
	Periods:          map[string]string{"calendar_day": "hoje", "rolling_1h": "na última hora", "rolling_24h": "nas últimas 24 horas", "rolling_7d": "nos últimos 7 dias", "lifetime": "até agora"},
	WarnNeverResets:  "Este total nunca é reposto. Adicione margem, altere a Card ou pause o seu agente.",
	WarnReviewWindow: "Reveja a margem deste período, altere a Card ou pause o seu agente.",
	WarnSubject:      "KIFF: aviso de orçamento de uma Card",
	WarnReview:       "Ver a Card",
}
