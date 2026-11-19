package words

// Spanish Activity lines (tú). The call is feminine, as in its other lines.
func init() {
	why := func(w string) string {
		if w == "" {
			return ""
		}
		return ": " + w
	}
	es.Activity = Activity{
		ByYou: "por ti", BySomeone: "por una persona de la cuenta",
		ByRole:      map[string]string{"admin": "por un administrador", "editor": "por un editor", "approver": "por un aprobador", "owner": "por el propietario"},
		ByOtherRole: func(string) string { return "por una persona de la cuenta" },

		WaitingForYou:  "Esperando tu respuesta",
		NotSent:        "No enviado",
		NotSentBecause: func(reason string) string { return "No enviado · " + reason },
		ApprovedOnceBy: func(by string) string { return "Aprobada una vez " + by },
		ExtraRoomBy:    func(by string) string { return "Margen extra dado " + by },
		RefusedBy:      func(by string) string { return "Rechazada " + by },

		AllowedByCard:    "Permitida por su Card",
		ApprovedBy:       func(by string) string { return "Aprobada " + by },
		ApprovedThenDone: func(by string) string { return "Aprobada " + by + " y después hecha" },
		DoneWithinCard:   "Hecho, dentro de su Card",
		ConnectionRefused: func(w string) string {
			return "No enviado: la conexión la rechazó antes de enviarla" + why(w) + ". Sigue contando para los límites de hoy hasta que KIFF pueda confirmar que no se hizo nada."
		},
		AppRefusedCounts: func(w string) string { return "La app la rechazó, y sigue contando para los límites de hoy" + why(w) },
		AppRefusedMoney:  "La app la rechazó. Cuenta para el total de hoy hasta que KIFF pueda confirmar que no se movió dinero.",
		AppRefusedOther:  "La app la rechazó. Cuenta para el total de hoy hasta que KIFF pueda confirmar que no se hizo nada.",
		NotSentToTool: func(allowed, w string) string {
			return allowed + ", pero no se pudo enviar: nada llegó a la herramienta" + why(w)
		},
		Sending: func(allowed string) string { return allowed + ", y enviada: esperando la respuesta de la herramienta" },
		AnswerLost: func(allowed string) string {
			return allowed + ", y enviada, pero la respuesta de la herramienta se perdió: revisa los registros de la propia herramienta"
		},
		FoundAtVendor: func(allowed string) string {
			return allowed + ", y enviada. KIFF no sabía cómo terminó, así que revisó los registros de la propia herramienta y la encontró allí: se realizó"
		},
		NotRecorded: func(allowed string) string {
			return allowed + ", pero no quedó registrado si la herramienta lo hizo: revisa los registros de la propia herramienta"
		},
		ConnectionWhy: map[string]string{
			"halt_unknown":           "no pudo comprobar si el agente estaba bloqueado",
			"halted":                 "el agente o la cuenta está bloqueado",
			"halted_before":          "la llamada se aprobó antes del último bloqueo",
			"not_granted":            "la conexión no incluye esta acción",
			"permit_invalid":         "el permiso de KIFF no coincidía con la llamada",
			"permit_expired":         "el permiso de KIFF caducó antes de que saliera la llamada",
			"bad_arguments":          "no se pudieron leer los argumentos de la llamada",
			"record_unavailable":     "no pudo guardar un registro de la llamada",
			"profile_not_applied":    "KIFF aún no ha aplicado los ajustes aprobados",
			"definition_invalid":     "la acción nombra un campo que la app no acepta",
			"definition_unread":      "no pudo comprobar la acción con la app",
			"operation_changed":      "la app cambió esta acción desde que se aprobó; la conexión no envía nada hasta que se vuelva a conectar",
			"signed_out":             "la app cerró la sesión; la conexión no envía nada hasta que vuelvas a iniciar sesión",
			"app_closed":             "esta app ya no se ofrece, así que su conexión está cerrada",
			"connection_unavailable": "la conexión, o su cuenta en la app, no se pudo usar en ese momento",
			"tool_not_served":        "esta acción nunca se ejecuta para esta app",
			"waits_for_key":          "esta acción necesita la clave de la app, y aún no se ha añadido",
			"waits_for_approval":     "esta acción se añadió a la conexión y aún espera tu aprobación",
		},
	}
}
