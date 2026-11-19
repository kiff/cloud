package pages

// watchPT is /tafs/watch's words in European Portuguese, at /pt/tafs/watch.
// The watch's own lines are quoted as tafs says them in Portuguese.
var watchPT = watchWords{
	Lang:        "pt",
	Title:       "tafs no seu relógio — KIFF",
	Description: "Veja o que precisa de si, recuse uma chamada retida, bloqueie um taf e pergunte ao Coach a partir do seu relógio. Aprovar continua a ser no telemóvel.",
	Badge:       "tafs · relógio",
	Head:        "tafs no seu relógio",
	Sub:         "Veja o que precisa de si, recuse uma chamada retida, bloqueie um taf e pergunte ao Coach, a partir do pulso. Aprovar continua a ser no telemóvel.",
	Does: watchSection{
		Head: "1. O que faz",
		Items: []string{
			"**Precisa de si:** quantas chamadas retidas precisam de si, e cada uma tal como o telemóvel a mostra: o seu taf, o que pede, o montante e porquê.",
			"**Os seus tafs:** o estado de cada taf numa linha.",
			"**Recusar** uma chamada retida, **Bloquear agora** um taf, ou **Bloquear tudo agora**, que o relógio lhe pede para confirmar mais uma vez.",
			"**Perguntar ao Coach** por voz.",
			"**Um mosaico** com quantos precisam de si.",
			"**Nunca se aprova no relógio.** Para uma chamada retida, um Card, um pagamento ou uma ligação, o relógio diz «Aprove no telemóvel» e abre-o lá.",
		},
	},
	Get: watchSection{
		Head: "2. Como obter",
		Items: []string{
			"**Wear OS: Em breve no Google Play.** Wear OS 3 ou posterior. A app do relógio vem com o tafs para Android: quando estiver no Google Play, instale-a a partir do Google Play no relógio.",
			"**Apple Watch: Em breve.** Virá com o tafs para iPhone e fará o mesmo, com a aprovação no seu iPhone.",
			"**Não se inicia sessão no relógio.** Usa o tafs no telemóvel, que tem de ter a sessão iniciada e estar perto.",
		},
	},
	Act: watchSection{
		Head: "3. Recusar e bloquear",
		Items: []string{
			"**O relógio precisa de um bloqueio de ecrã, e tem de estar desbloqueado.** Caso contrário, diz: «Para recusar ou bloquear a partir deste relógio, defina um bloqueio de ecrã e desbloqueie-o.»",
			"**É o telemóvel que o faz,** com a sua própria chave, e o relógio mostra o resultado quando terminar, como «Recusado. Nada foi enviado.»",
			"Se alguém respondeu à chamada entretanto, o relógio diz «Já respondido, ou terminado.»",
		},
	},
	Coach: watchSection{
		Head: "4. Perguntar ao Coach",
		Items: []string{
			"Toque em **Perguntar ao Coach** e fale. A introdução por voz do relógio transforma o que diz em texto, e o relógio mostra-o antes de enviar o que quer que seja: **Enviar** ou **Cancelar**.",
			"**O Coach recebe as suas palavras como texto, nunca a sua voz.**",
			"Primeiro, permita a utilização de IA no tafs no telemóvel, e inicie o Coach lá.",
			"Uma resposta curta é mostrada no relógio. Uma longa, ou uma que lhe pede uma resposta, mostra o início e **Abrir no telemóvel**.",
			"Tal como para recusar e bloquear, o relógio precisa de um bloqueio de ecrã e tem de estar desbloqueado.",
		},
	},
	Locked: watchSection{
		Head: "5. Quando o relógio está bloqueado",
		Items: []string{
			"O mosaico mostra apenas «tafs», e um alerta mostra apenas «Um taf precisa de si». O Wear OS bloqueia o relógio quando o tira do pulso.",
			"Desbloqueado, um alerta mostra o que mostra o do telemóvel: que taf e o que pede, o que pode indicar um montante, um comerciante ou uma pessoa.",
			"Num Apple Watch, os alertas são os do seu iPhone, por isso é a definição de pré-visualizações do iPhone que decide o que mostram. Com as pré-visualizações ocultas, que é a definição do iPhone quando está bloqueado, a menos que a altere, um alerta mostra apenas «Um taf precisa de si».",
		},
	},
	Keeps: watchSection{
		Head: "6. O que guarda",
		Items: []string{
			"Apenas quantos precisam de si, para o mosaico. Nunca é mostrado enquanto o relógio está bloqueado, e é então apagado, tal como quando termina sessão no telemóvel.",
			"Sem sessão, sem chaves. O que mostra é esquecido quando o põe de lado, e nada do que diz é guardado.",
		},
		Link: watchLink{Before: "Os detalhes estão na ", Text: "política de privacidade do tafs", After: "."},
	},
	Help: watchSection{
		Head: "7. Se algo não estiver bem",
		Items: []string{
			"«O telemóvel não está ao alcance. Aproxime-o e tente novamente.»",
			"«Primeiro, inicie sessão no tafs no telemóvel.»",
		},
		Link: watchLink{Before: "Tudo o resto: **Abrir no telemóvel**, ou ", Text: "escreva-nos", After: "."},
	},
}
