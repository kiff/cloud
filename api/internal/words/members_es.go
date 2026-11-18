package words

// Spanish membership emails (tú).
func init() {
	es.Membership = Membership{
		AnAccount:      "una cuenta de KIFF",
		AccountNamed:   func(name string) string { return "la cuenta de KIFF que sus administradores llamaron «" + name + "»" },
		UnnamedAccount: "Cuenta sin nombre",
		InviteSubject: func(name string) string {
			if name == "" {
				return "Te han invitado a una cuenta en KIFF"
			}
			return "Te han invitado a «" + name + "» en KIFF"
		},
		InviteIntro: func(by, account, role, what string) string {
			return by + " te invitó a " + account + ", como " + role + ", para que puedas " + what + "."
		},
		InviteRoleWhat: map[string]string{
			"admin":    "gestionar la cuenta: personas, claves, facturación y políticas, además de Cards y dominios",
			"editor":   "definir qué pueden hacer los agentes de la cuenta: Cards, dominios y herramientas",
			"approver": "responder a las acciones retenidas de la cuenta",
			"viewer":   "ver los agentes, las Cards y la actividad de la cuenta sin cambiar nada",
		},
		RoleName:        map[string]string{"admin": "administrador", "editor": "editor", "approver": "aprobador", "viewer": "lector"},
		InviteRoleOther: "trabajar en la cuenta",
		InviteAccept:    func(url string) string { return "Inicia sesión con cualquier dirección y acéptala aquí: " + url },
		InviteButton:    "Ver y aceptar la invitación",
		InviteExpires:   func(when string) string { return "La invitación caduca el " + when + "." },
		InviteIgnore:    "Si no lo esperabas, ignóralo; no pasa nada a menos que aceptes.",

		AnAdmin: "Un administrador",
		Someone: "Alguien",
		AMember: "Un miembro",

		PeopleLink:       "Ver las personas de la cuenta",
		NoticeWhy:        "Te llega porque eres administrador o aprobador de la cuenta.",
		AuthoritySubject: func(role, account string) string { return "Nuevo " + role + " en " + account },
		AuthorityInvited: func(who, role, account, by string) string {
			return "Se envió una invitación a " + who + " como " + role + " en " + account + ", por " + by + "."
		},
		AuthorityChanged: func(who, from, role, account, by string) string {
			return who + " pasó de " + from + " a " + role + " en " + account + ", por " + by + "."
		},
		JoinedSubject: func(account string) string { return "Alguien se unió a " + account },
		JoinedUnknown: func(invited, role, account string) string {
			return "La invitación enviada a " + invited + " fue aceptada, y la persona se unió a " + account + " como " + role + "."
		},
		JoinedOther: func(invited, who, role, account string) string {
			return "La invitación enviada a " + invited + " fue aceptada por " + who + ", otra dirección, y la persona se unió a " + account + " como " + role + "."
		},
		JoinedSelf: func(who, role, account string) string {
			return who + " aceptó su invitación y se unió a " + account + " como " + role + "."
		},
		CardChangeSubject: "Se cambió una Card para dejar pasar una acción retenida",
		CardChangeBody: func(who, agent, action, entity string) string {
			card := "una Card"
			if agent != "" {
				card = "una Card de " + agent
			}
			out := who + " cambió " + card + " para que la acción retenida " + action
			if entity != "" {
				out += " sobre " + entity
			}
			return out + " pudiera pasar. La separación de funciones está activada, así que es un cambio de Card, no una aprobación de un aprobador; queda registrado en la Card."
		},
		CardChangeCards: func(cards string) string { return "Card: " + cards + "." },
		SoDOffSubject:   "Se desactivó la separación de funciones",
		SoDOffBody: func(who, account string) string {
			return who + " desactivó la separación de funciones en " + account + ". Ahora los administradores y los editores también pueden responder a las acciones retenidas."
		},

		YourPhone:     "Tu teléfono",
		RelinkSubject: func(phone string) string { return phone + " puede volver a aprobar, con una clave nueva" },
		RelinkBody: func(phone string) string {
			return phone + " puede volver a aprobar en KIFF, con una clave nueva para la huella o la cara. " +
				"Lo aprobaste en tu navegador hace un momento. Si no fuiste tú, elimina el dispositivo en KIFF y vuelve a iniciar sesión."
		},
		NewApproverSubject: func(phone string) string { return phone + " ya puede aprobar en KIFF" },
		NewApproverBody: func(phone string) string {
			return phone + " ya puede aprobar en KIFF, con la huella o la cara. Lo aprobaste en tu navegador hace un momento, mientras otro de tus dispositivos ya aprueba. Si no fuiste tú, elimina el dispositivo en KIFF y cambia tu contraseña."
		},

		PackAskSubject:  "Una solicitud de paquete de créditos de tafs",
		PackAskBody:     func(who, pack string) string { return who + " pide un paquete de créditos: " + pack + "." },
		RaiseAskSubject: "Una solicitud para subir el límite de gasto de tafs",
		RaiseAskBody: func(who string) string {
			return who + " te pide subir el límite de gasto, para que los tafs de la cuenta puedan seguir."
		},
		AskReason: func(reason string) string { return "Su motivo: " + reason },
		PackAskHow: func(url string) string {
			return "Aprueba añadiendo el paquete, o recházalo, en tafs o en kiff.dev: " + url
		},
		RaiseAskHow: "Súbelo, o recházalo, en tafs.",
	}
}
