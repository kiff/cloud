package words

// The code sign-in emails in es. A draft for the languages' owner to correct.
func init() {
	es.CodeSignInEmail = CodeSignInEmail{
		CodeSubject:          "Tu código para iniciar sesión en KIFF",
		CodeIntro:            "Alguien pidió iniciar sesión en KIFF con un código enviado a esta dirección. Tu código:",
		CodeValid:            "Funciona una vez, durante 10 minutos, en el navegador que lo pidió.",
		CodeNeverShare:       "No le des este código a nadie. KIFF nunca te lo pedirá.",
		CodeNotYou:           "Si no fuiste tú, ignora este correo. Nadie puede iniciar sesión sin el código.",
		CodeAskedWhen:        "Cuándo",
		CodeAskedBrowser:     "Navegador",
		CodeAskedPlace:       "Ubicación aproximada",
		CodeOffSubject:       "Iniciar sesión con un código no está activado en tu cuenta de KIFF",
		CodeOffIntro:         "Alguien pidió iniciar sesión en KIFF con un código enviado a esta dirección. Iniciar sesión con un código no está activado en tu cuenta, así que no se envió ningún código.",
		CodeOffHow:           "Para usarlo, inicia sesión de otra forma y activa «Iniciar sesión con un código enviado por correo» en los ajustes de inicio de sesión de tu cuenta.",
		CodeOffNotYou:        "Si no fuiste tú, ignora este correo. No ha cambiado nada.",
		CodeTurnedOnSubject:  "Iniciar sesión con un código ya está activado en tu cuenta de KIFF",
		CodeTurnedOnIntro:    "Se activó el inicio de sesión con un código enviado por correo en tu cuenta de KIFF.",
		CodeTurnedOffSubject: "Iniciar sesión con un código ya está desactivado en tu cuenta de KIFF",
		CodeTurnedOffIntro:   "Se desactivó el inicio de sesión con un código enviado por correo en tu cuenta de KIFF.",
		CodeChangedNotYou:    "Si no fuiste tú, inicia sesión de otra forma, revisa los ajustes de inicio de sesión de tu cuenta y cierra la sesión en los dispositivos que no conozcas.",
		CodeWhy:              "KIFF envía este correo cuando alguien pide iniciar sesión con un código enviado a esta dirección.",
	}
}
