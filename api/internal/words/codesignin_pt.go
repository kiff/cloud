package words

// The code sign-in emails in pt. A draft for the languages' owner to correct.
func init() {
	pt.CodeSignInEmail = CodeSignInEmail{
		CodeSubject:          "O seu código para iniciar sessão no KIFF",
		CodeIntro:            "Alguém pediu para iniciar sessão no KIFF com um código enviado para este endereço. O seu código:",
		CodeValid:            "Funciona uma vez, durante 10 minutos, no navegador que o pediu.",
		CodeNeverShare:       "Nunca dê este código a ninguém. O KIFF nunca lho vai pedir.",
		CodeNotYou:           "Se não foi você, ignore este email. Ninguém consegue iniciar sessão sem o código.",
		CodeAskedWhen:        "Quando",
		CodeAskedBrowser:     "Navegador",
		CodeAskedPlace:       "Localização aproximada",
		CodeOffSubject:       "Iniciar sessão com um código não está ativo na sua conta KIFF",
		CodeOffIntro:         "Alguém pediu para iniciar sessão no KIFF com um código enviado para este endereço. Iniciar sessão com um código não está ativo na sua conta, por isso não foi enviado nenhum código.",
		CodeOffHow:           "Para o usar, inicie sessão de outra forma e ative «Iniciar sessão com um código enviado por email» nas definições de início de sessão da sua conta.",
		CodeOffNotYou:        "Se não foi você, ignore este email. Nada mudou.",
		CodeTurnedOnSubject:  "Iniciar sessão com um código está agora ativo na sua conta KIFF",
		CodeTurnedOnIntro:    "O início de sessão com um código enviado por email foi ativado na sua conta KIFF.",
		CodeTurnedOffSubject: "Iniciar sessão com um código está agora desativado na sua conta KIFF",
		CodeTurnedOffIntro:   "O início de sessão com um código enviado por email foi desativado na sua conta KIFF.",
		CodeChangedNotYou:    "Se não foi você, inicie sessão de outra forma, verifique as definições de início de sessão da sua conta e termine a sessão nos dispositivos que não conhece.",
		CodeWhy:              "O KIFF envia este email quando alguém pede para iniciar sessão com um código enviado para este endereço.",
	}
}
