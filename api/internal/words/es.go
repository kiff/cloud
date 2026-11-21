package words

import "fmt"

// Spanish (tú), neutral, in the glossary's terms.
var es = Words{
	DecimalComma: true,

	WindowNextApproval:     "Tu huella o tu cara en la próxima aprobación",
	WindowAgainUnderMinute: "Tu huella o tu cara de nuevo en menos de un minuto",
	WindowNoneNextMinute:   "Sin huella ni cara durante el próximo minuto",
	WindowNoneMinutes:      func(n int) string { return fmt.Sprintf("Sin huella ni cara durante los próximos %d minutos", n) },

	Today:     "hoy",
	Yesterday: "ayer",
	Months:    [12]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"},

	HappenedYes:           "Sí, se hizo",
	HappenedYesDetail:     "Sigue contando en la Card.",
	HappenedNo:            "No, no se hizo",
	HappenedNoDetail:      "La Card recupera su margen. En el proveedor no se revierte nada.",
	HappenedNotSure:       "No estoy seguro, ayúdame a comprobarlo",
	HappenedNotSureDetail: "Coach mira contigo los registros del proveedor. La pregunta queda contigo un día.",
	HappenedNoAfter:       "La Card recupera su margen. En el proveedor no se ha revertido nada: comprueba allí si el pago llegó a hacerse y, si fue así, díselo a Coach.",
	WhyLost: func(vendor string, read bool) string {
		of := "del proveedor"
		if vendor != "" {
			of = "de " + vendor
		}
		if read {
			return "Se envió, pero su respuesta se perdió, y las lecturas de KIFF en los registros " + of + " aún no lo han resuelto."
		}
		return "Se envió, pero su respuesta se perdió, y en los registros " + of + " no se puede buscar esta llamada."
	},
	WhyRefused: func(vendor string, read bool) string {
		if vendor == "" {
			vendor = "el proveedor"
		}
		if read {
			return Capitalize(vendor) + " la rechazó sin decir si se cobró algo, y las lecturas de KIFF en sus registros aún no lo han resuelto."
		}
		return Capitalize(vendor) + " la rechazó sin decir si se cobró algo, y en sus registros no se puede buscar esta llamada."
	},
	WhyProbation: "Es una de las primeras llamadas de una conexión que ninguna cuenta de prueba ha comprobado: confirma qué pasó.",
	StatusRefused: func(vendor string) string {
		if vendor == "" {
			return "El proveedor la rechazó. Comprueba con el proveedor si se movió dinero."
		}
		return Capitalize(vendor) + " la rechazó. Comprueba en " + vendor + " si se movió dinero."
	},
	StatusLost: func(vendor string) string {
		if vendor == "" {
			return "Se envió al proveedor, pero su respuesta se perdió. Comprueba con el proveedor si se hizo."
		}
		return "Se envió a " + vendor + ", pero su respuesta se perdió. Comprueba en " + vendor + " si se hizo."
	},
	StatusProbation: "Una de las primeras llamadas por esta conexión: comprueba que hizo lo que esperabas.",
	CallAtVendor:    func(action, vendor string) string { return action + " en " + vendor },
	CallByTaf:       func(taf string) string { return "por " + taf },

	Reasons: map[string]string{
		"mandate_owner_rejected":       "La rechazaste.",
		"mandate_owner_expired":        "Nadie la respondió a tiempo.",
		"mandate_withdrawn":            "Se retiró antes de que nadie respondiera.",
		"mandate_per_action_exceeded":  "Superaba el límite de la Card para una llamada.",
		"mandate_limit_reached":        "Superaba uno de los totales de la Card.",
		"mandate_resource_not_on_card": "Actuaba sobre algo que la Card no cubre.",
		"mandate_unreadable_resource":  "KIFF no pudo leer sobre qué actuaba.",
		"mandate_not_covered":          "Ninguna Card cubre esta herramienta.",
		"mandate_expired":              "Su Card ha caducado.",
		"no_mandate_issued":            "Ninguna Card cubre esta herramienta.",
		"amount_unit_mismatch":         "Era en otra moneda que la de la Card.",
		"amount_unit_unverified":       "KIFF no pudo comprobar su moneda.",
		"amount_unverified":            "KIFF no pudo leer su importe.",
		"halted":                       "Lo habías bloqueado.",
		"halt_unavailable":             "KIFF no pudo comprobar si estaba bloqueado.",
	},
	OwnerAsks: map[string]string{
		OwnerAskSealedMoney:  "esta herramienta mueve dinero por una conexión cuya clave guarda KIFF, así que cada llamada espera una aprobación",
		OwnerAskSecondRefund: "este cobro ya tiene un reembolso, así que un segundo espera una aprobación",
		OwnerAskNoRequestID:  "el agente no indicó ninguna solicitud de una app conectada, así que este reembolso espera una aprobación",
		OwnerAskOther:        "cada llamada de esta herramienta espera una aprobación",
		OwnerAskEachCall:     "cada llamada de esta herramienta espera una aprobación",
	},
	ResourceMissing: func(param string) string {
		return fmt.Sprintf("la llamada no indica ningún %s, y esta Card solo se aplica a los %s que lista", param, param)
	},
	ResourceNotOnCard: func(param, value string) string {
		return fmt.Sprintf("%s %q no es uno de los que cubre esta Card", param, value)
	},
	OverPerCall: func(draw, limit string) string {
		return fmt.Sprintf("%s supera el límite de %s por llamada", draw, limit)
	},
	OverTotal: func(draw, limit, used string) string {
		return fmt.Sprintf("%s superaría el límite de %s: ya se usaron %s", draw, limit, used)
	},

	// The held-call and Card-warning emails, and a warning's reason.
	HeldSubject:     "Tu agente espera tu respuesta",
	HeldNothingDone: "No se ha hecho nada.",
	HeldOpen:        "Ábrelo en KIFF para responder (se te pedirá que inicies sesión):",
	HeldOptions:     "Puedes aprobarlo una vez, cambiar la Card para permitirlo o rechazarlo.",
	HeldIntro: func(agent, action, amount string) string {
		return agent + " quiere ejecutar " + action + amount + ", que está fuera de su Card."
	},
	HeldSteps: func(steps string) string {
		return "La Card también te permite rechazarlo con un siguiente paso para el agente: " + steps + "."
	},
	HeldExpires: func(when string) string {
		return "Si nadie responde antes del " + when + ", se rechaza y el agente tiene que volver a pedirlo."
	},
	HeldNoApprove:   "Desde este correo no se puede aprobar nada.",
	HeldButtonsNote: "Cada botón abre esta acción en KIFF, donde inicias sesión y confirmas tu respuesta.",
	HeldApproveOnce: "Aprobar una vez",
	HeldChangeCard:  "Cambiar la Card para permitirlo",
	HeldReject:      "Rechazar",
	WarnSpent:       "Este total de la Card está agotado.",
	WarnBurst:       "Una Card nueva usó más que su umbral de pico al inicio de este periodo.",
	WarnRunway:      "Al ritmo reciente, este total de la Card se está agotando.",
	WarnUnusual: func(ratio string) string {
		return "El uso reciente es " + ratio + " veces su promedio equivalente de siete días."
	},
	WarnUsed:         func(pct, period string) string { return "Se ha usado el " + pct + " % de este total " + period + "." },
	Periods:          map[string]string{"calendar_day": "hoy", "rolling_1h": "en la última hora", "rolling_24h": "en las últimas 24 horas", "rolling_7d": "en los últimos 7 días", "lifetime": "hasta ahora"},
	WarnNeverResets:  "Este total nunca se reinicia. Añade margen, cambia la Card o pausa su agente.",
	WarnReviewWindow: "Revisa el margen de este periodo, cambia la Card o pausa su agente.",
	WarnSubject:      "KIFF: aviso de presupuesto de una Card",
	WarnReview:       "Revisar la Card",
}
