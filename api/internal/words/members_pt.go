package words

// Portuguese membership emails (pt-PT, você).
func init() {
	pt.Membership = Membership{
		AnAccount:      "uma conta KIFF",
		AccountNamed:   func(name string) string { return "a conta KIFF a que os administradores deram o nome «" + name + "»" },
		UnnamedAccount: "Conta sem nome",
		InviteSubject: func(name string) string {
			if name == "" {
				return "Tem um convite para uma conta no KIFF"
			}
			return "Tem um convite para «" + name + "» no KIFF"
		},
		InviteIntro: func(by, account, role, what string) string {
			return by + " enviou-lhe um convite para " + account + ", como " + role + ", para poder " + what + "."
		},
		InviteRoleWhat: map[string]string{
			"admin":    "gerir a conta: pessoas, chaves, faturação e regras, além de Cards e domínios",
			"editor":   "definir o que os agentes da conta podem fazer: Cards, domínios e ferramentas",
			"approver": "responder às ações retidas da conta",
			"viewer":   "ver os agentes, as Cards e a atividade da conta sem alterar nada",
		},
		RoleName:        map[string]string{"admin": "administrador", "editor": "editor", "approver": "aprovador", "viewer": "leitor"},
		InviteRoleOther: "trabalhar na conta",
		InviteAccept:    func(url string) string { return "Inicie sessão com qualquer endereço e aceite-o aqui: " + url },
		InviteButton:    "Ver e aceitar o convite",
		InviteExpires:   func(when string) string { return "O convite expira a " + when + "." },
		InviteIgnore:    "Se não estava à espera disto, ignore-o; nada acontece a menos que aceite.",

		AnAdmin: "Um administrador",
		Someone: "Alguém",
		AMember: "Um membro",

		PeopleLink:       "Ver as pessoas da conta",
		NoticeWhy:        "Recebe isto por ser administrador ou aprovador da conta.",
		AuthoritySubject: func(role, account string) string { return "Novo " + role + " em " + account },
		AuthorityInvited: func(who, role, account, by string) string {
			return "Foi enviado um convite a " + who + " como " + role + " em " + account + ", por " + by + "."
		},
		AuthorityChanged: func(who, from, role, account, by string) string {
			return who + " passou de " + from + " a " + role + " em " + account + ", por " + by + "."
		},
		JoinedSubject: func(account string) string { return "Alguém entrou em " + account },
		JoinedUnknown: func(invited, role, account string) string {
			return "O convite enviado para " + invited + " foi aceite, e a pessoa entrou em " + account + " como " + role + "."
		},
		JoinedOther: func(invited, who, role, account string) string {
			return "O convite enviado para " + invited + " foi aceite por " + who + ", outro endereço, e a pessoa entrou em " + account + " como " + role + "."
		},
		JoinedSelf: func(who, role, account string) string {
			return who + " aceitou o convite e entrou em " + account + " como " + role + "."
		},
		CardChangeSubject: "Uma Card foi alterada para deixar passar uma ação retida",
		CardChangeBody: func(who, agent, action, entity string) string {
			card := "uma Card"
			if agent != "" {
				card = "uma Card de " + agent
			}
			out := who + " alterou " + card + " para que a ação retida " + action
			if entity != "" {
				out += " em " + entity
			}
			return out + " pudesse avançar. A separação de funções está ativa, por isso é uma alteração da Card, não uma aprovação de um aprovador; fica registada na Card."
		},
		CardChangeCards: func(cards string) string { return "Card: " + cards + "." },
		SoDOffSubject:   "A separação de funções foi desativada",
		SoDOffBody: func(who, account string) string {
			return who + " desativou a separação de funções em " + account + ". Agora os administradores e os editores também podem responder às ações retidas."
		},

		YourPhone:     "O seu telemóvel",
		RelinkSubject: func(phone string) string { return phone + " pode voltar a aprovar, com uma chave nova" },
		RelinkBody: func(phone string) string {
			return phone + " pode voltar a aprovar no KIFF, com uma chave nova para a impressão digital ou o rosto. " +
				"Aprovou-o no seu navegador agora mesmo. Se não foi você, remova o dispositivo no KIFF e inicie sessão novamente."
		},
		NewApproverSubject: func(phone string) string { return phone + " já pode aprovar no KIFF" },
		NewApproverBody: func(phone string) string {
			return phone + " já pode aprovar no KIFF, com a impressão digital ou o rosto. Aprovou-o no seu navegador agora mesmo, enquanto outro dos seus dispositivos já aprova. Se não foi você, remova o dispositivo no KIFF e altere a sua palavra-passe."
		},

		PackAskSubject:  "Um pedido de pacote de créditos tafs",
		PackAskBody:     func(who, pack string) string { return who + " pede um pacote de créditos: " + pack + "." },
		RaiseAskSubject: "Um pedido para aumentar o limite de gastos do tafs",
		RaiseAskBody: func(who string) string {
			return who + " pede-lhe que aumente o limite de gastos, para que os tafs da conta possam continuar."
		},
		AskReason: func(reason string) string { return "O motivo: " + reason },
		PackAskHow: func(url string) string {
			return "Aprove adicionando o pacote, ou recuse, no tafs ou em kiff.dev: " + url
		},
		RaiseAskHow: "Aumente-o, ou recuse, no tafs.",
	}
}
