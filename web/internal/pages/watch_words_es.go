package pages

// watchES is /tafs/watch's words in Spanish, at /es/tafs/watch. The watch's
// own lines are quoted as tafs says them in Spanish.
var watchES = watchWords{
	Lang:        "es",
	Title:       "tafs en tu reloj — KIFF",
	Description: "Mira lo que te necesita, rechaza una llamada retenida, bloquea un taf y pregunta a Coach desde tu reloj. La aprobación se queda en tu teléfono.",
	Badge:       "tafs · reloj",
	Head:        "tafs en tu reloj",
	Sub:         "Mira lo que te necesita, rechaza una llamada retenida, bloquea un taf y pregunta a Coach, desde tu muñeca. La aprobación se queda en tu teléfono.",
	Does: watchSection{
		Head: "1. Qué hace",
		Items: []string{
			"**Te necesita:** cuántas llamadas retenidas te necesitan, y cada una tal como la muestra tu teléfono: su taf, qué quiere, el importe y por qué.",
			"**Tus tafs:** el estado de cada taf en una línea.",
			"**Rechazar** una llamada retenida, **Bloquear ahora** un taf, o **Bloquear todo ahora**, que el reloj te pide confirmar una vez más.",
			"**Preguntar a Coach** por voz.",
			"**Una tarjeta** con cuántos te necesitan.",
			"**Nunca se aprueba en el reloj.** Para una llamada retenida, una Card, un pago o una conexión, el reloj dice «Aprueba en tu teléfono» y lo abre allí.",
		},
	},
	Get: watchSection{
		Head: "2. Cómo conseguirlo",
		Items: []string{
			"**Wear OS: Muy pronto en Google Play.** Wear OS 3 o posterior. La app del reloj viene con tafs para Android: cuando esté en Google Play, instálala desde Google Play en tu reloj.",
			"**Apple Watch: Llegará.** Vendrá con tafs para iPhone y hará lo mismo, con la aprobación en tu iPhone.",
			"**No se inicia sesión en el reloj.** Usa tafs en tu teléfono, que tiene que tener la sesión iniciada y estar cerca.",
		},
	},
	Act: watchSection{
		Head: "3. Rechazar y bloquear",
		Items: []string{
			"**Tu reloj necesita un bloqueo de pantalla, y tiene que estar desbloqueado.** Si no, dice: «Para rechazar o bloquear desde este reloj, pon un bloqueo de pantalla y desbloquéalo.»",
			"**Lo hace tu teléfono,** con su propia clave, y el reloj muestra el resultado cuando termina, como «Rechazada. No se envió nada.»",
			"Si alguien respondió la llamada mientras tanto, el reloj dice «Ya respondida, o terminada.»",
		},
	},
	Coach: watchSection{
		Head: "4. Preguntar a Coach",
		Items: []string{
			"Toca **Preguntar a Coach** y habla. El dictado por voz de tu reloj convierte lo que dices en texto, y el reloj lo muestra antes de enviar nada: **Enviar** o **Cancelar**.",
			"**Coach recibe tus palabras como texto, nunca tu voz.**",
			"Primero, permite el uso de IA en tafs en tu teléfono, e inicia Coach allí.",
			"Una respuesta corta se muestra en el reloj. Una larga, o una que te pide responder algo, muestra su inicio y **Abrir en el teléfono**.",
			"Como para rechazar y bloquear, tu reloj necesita un bloqueo de pantalla y tiene que estar desbloqueado.",
		},
	},
	Locked: watchSection{
		Head: "5. Cuando tu reloj está bloqueado",
		Items: []string{
			"La tarjeta solo muestra «tafs», y una alerta solo muestra «Un taf te necesita». Wear OS bloquea tu reloj cuando te lo quitas de la muñeca.",
			"Desbloqueado, una alerta muestra lo que muestra la de tu teléfono: qué taf y qué quiere, lo que puede nombrar un importe, un comercio o una persona.",
			"En un Apple Watch, las alertas son las de tu iPhone, así que el ajuste de previsualizaciones de tu iPhone decide lo que muestran. Con las previsualizaciones ocultas, que es el ajuste del iPhone cuando está bloqueado salvo que lo cambies, una alerta solo muestra «Un taf te necesita».",
		},
	},
	Keeps: watchSection{
		Head: "6. Qué guarda",
		Items: []string{
			"Solo cuántos te necesitan, para la tarjeta. Nunca se muestra mientras tu reloj está bloqueado, y entonces se borra, igual que cuando cierras sesión en tu teléfono.",
			"Ni sesión, ni claves. Lo que muestra se olvida cuando lo apartas, y no se guarda nada de lo que dices.",
		},
		Link: watchLink{Before: "Los detalles están en la ", Text: "política de privacidad de tafs", After: "."},
	},
	Help: watchSection{
		Head: "7. Si algo no va bien",
		Items: []string{
			"«Tu teléfono no está al alcance. Acércalo y vuelve a intentarlo.»",
			"«Primero inicia sesión en tafs en tu teléfono.»",
		},
		Link: watchLink{Before: "Cualquier otra cosa: **Abrir en el teléfono**, o ", Text: "escríbenos", After: "."},
	},
}
