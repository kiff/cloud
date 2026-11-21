# Where KIFF is available: refusal words and terms clause (drafts)

The owner's rule: KIFF and tafs are offered only where Anthropic's Claude is available
(https://www.anthropic.com/supported-countries). These are the words for the region check at sign-up and on the API,
and the clause for both sets of terms. The check's code takes the keys as named here; the terms take the clause as a
new section. `{country}` is the country's name in the page's language.

## 1. Refusals

- `region.unsupported_here`: the IP country isn't supported (where the person is now).
- `region.unsupported_country`: the account's stated country isn't supported.

| lang | `region.unsupported_here` | `region.unsupported_country` |
|---|---|---|
| en | KIFF isn't available where you are right now. We offer it only in the countries and regions where Anthropic's Claude is available. | KIFF isn't available in this country: {country}. We offer it only in the countries and regions where Anthropic's Claude is available. |
| de | KIFF ist dort, wo du gerade bist, nicht verfügbar. Wir bieten es nur in den Ländern und Regionen an, in denen Claude von Anthropic verfügbar ist. | KIFF ist in diesem Land nicht verfügbar: {country}. Wir bieten es nur in den Ländern und Regionen an, in denen Claude von Anthropic verfügbar ist. |
| es | KIFF no está disponible donde estás ahora. Solo lo ofrecemos en los países y regiones donde Claude de Anthropic está disponible. | KIFF no está disponible en este país: {country}. Solo lo ofrecemos en los países y regiones donde Claude de Anthropic está disponible. |
| fr | KIFF n'est pas disponible là où vous êtes en ce moment. Nous le proposons uniquement dans les pays et régions où Claude d'Anthropic est disponible. | KIFF n'est pas disponible dans ce pays : {country}. Nous le proposons uniquement dans les pays et régions où Claude d'Anthropic est disponible. |
| it | KIFF non è disponibile dove ti trovi adesso. Lo offriamo solo nei paesi e nelle regioni in cui Claude di Anthropic è disponibile. | KIFF non è disponibile in questo paese: {country}. Lo offriamo solo nei paesi e nelle regioni in cui Claude di Anthropic è disponibile. |
| nl | KIFF is niet beschikbaar waar je nu bent. We bieden het alleen aan in de landen en regio's waar Claude van Anthropic beschikbaar is. | KIFF is niet beschikbaar in dit land: {country}. We bieden het alleen aan in de landen en regio's waar Claude van Anthropic beschikbaar is. |
| pl | KIFF nie jest dostępny tam, gdzie teraz jesteś. Oferujemy go tylko w krajach i regionach, w których dostępny jest Claude firmy Anthropic. | KIFF nie jest dostępny w tym kraju: {country}. Oferujemy go tylko w krajach i regionach, w których dostępny jest Claude firmy Anthropic. |
| pt | O KIFF não está disponível onde se encontra neste momento. Só o disponibilizamos nos países e regiões onde o Claude da Anthropic está disponível. | O KIFF não está disponível neste país: {country}. Só o disponibilizamos nos países e regiões onde o Claude da Anthropic está disponível. |
| ko | 지금 계신 곳에서는 KIFF를 이용할 수 없습니다. KIFF는 Anthropic의 Claude를 이용할 수 있는 국가와 지역에서만 제공됩니다. | (to come) |
| ja | 現在地ではKIFFをご利用いただけません。KIFFは、AnthropicのClaudeが利用できる国と地域でのみ提供しています。 | (to come) |

ko and ja are on the API's refusal only, the line the apps show. The web's refused page and the stated-country line
follow once kiff.dev speaks them.

The API's refusal keeps a stable `code` (`region_unsupported`) beside the words, so the apps never match on text.

## 2. Terms clause (KIFF's Terms of Service and the tafs Terms)

**en — Where KIFF is available.** You may use KIFF and tafs only in the countries and regions where Anthropic's Claude
is available, as Anthropic lists them, and not while you are physically anywhere else. You may not use them for, or
on behalf of, an organisation that is incorporated or headquartered outside those countries and regions, or that is
majority-owned or controlled, directly or indirectly, by such an organisation or by persons located there. We may
refuse a sign-up, or suspend an account, that doesn't meet this, and we tell you why.

**de — Wo KIFF verfügbar ist.** Du darfst KIFF und tafs nur in den Ländern und Regionen nutzen, in denen Claude von
Anthropic verfügbar ist, wie Anthropic sie auflistet, und nicht, während du dich körperlich anderswo aufhältst. Du darfst
sie nicht für oder im Namen einer Organisation nutzen, die außerhalb dieser Länder und Regionen gegründet ist oder dort
ihren Hauptsitz hat, oder die direkt oder indirekt mehrheitlich einer solchen Organisation oder dort ansässigen Personen
gehört oder von ihnen kontrolliert wird. Wir können eine Registrierung ablehnen oder ein Konto sperren, das dies nicht
erfüllt, und sagen dir, warum.

**es — Dónde está disponible KIFF.** Solo puedes usar KIFF y tafs en los países y regiones donde Claude de Anthropic
está disponible, según la lista de Anthropic, y no mientras te encuentres físicamente en otro lugar. No puedes usarlos
para una organización, ni en su nombre, que esté constituida o tenga su sede fuera de esos países y regiones, o que sea
propiedad mayoritaria o esté controlada, directa o indirectamente, por una organización así o por personas que se
encuentren allí. Podemos rechazar un registro o suspender una cuenta que no cumpla esto, y te diremos por qué.

**fr — Où KIFF est disponible.** Vous ne pouvez utiliser KIFF et tafs que dans les pays et régions où Claude
d'Anthropic est disponible, tels qu'Anthropic les liste, et non lorsque vous vous trouvez physiquement ailleurs. Vous ne
pouvez pas les utiliser pour une organisation, ou en son nom, qui est constituée ou a son siège en dehors de ces pays et
régions, ou qui est détenue majoritairement ou contrôlée, directement ou indirectement, par une telle organisation ou
par des personnes qui s'y trouvent. Nous pouvons refuser une inscription, ou suspendre un compte, qui ne respecte pas
cette règle, et nous vous en indiquons la raison.

**it — Dove è disponibile KIFF.** Puoi usare KIFF e tafs solo nei paesi e nelle regioni in cui Claude di Anthropic è
disponibile, secondo l'elenco di Anthropic, e non mentre ti trovi fisicamente altrove. Non puoi usarli per conto di
un'organizzazione costituita o con sede al di fuori di tali paesi e regioni, o posseduta a maggioranza o controllata,
direttamente o indirettamente, da tale organizzazione o da persone che vi si trovano. Possiamo rifiutare una
registrazione, o sospendere un account, che non rispetti questa regola, e ti diciamo perché.

**nl — Waar KIFF beschikbaar is.** Je mag KIFF en tafs alleen gebruiken in de landen en regio's waar Claude van
Anthropic beschikbaar is, zoals Anthropic ze vermeldt, en niet terwijl je je fysiek ergens anders bevindt. Je mag ze
niet gebruiken voor of namens een organisatie die buiten die landen en regio's is opgericht of daar haar hoofdkantoor
heeft, of die direct of indirect in meerderheid eigendom is van, of gecontroleerd wordt door, zo'n organisatie of
personen die zich daar bevinden. We kunnen een aanmelding weigeren, of een account opschorten, dat hier niet aan voldoet,
en we vertellen je waarom.

**pl — Gdzie KIFF jest dostępny.** Możesz korzystać z KIFF i tafs tylko w krajach i regionach, w których dostępny jest
Claude firmy Anthropic, zgodnie z listą Anthropic, i nie wtedy, gdy fizycznie przebywasz gdzie indziej. Nie możesz
korzystać z nich dla organizacji ani w imieniu organizacji, która została założona lub ma siedzibę poza tymi krajami i
regionami albo która jest bezpośrednio lub pośrednio w większości własnością takiej organizacji lub przebywających tam
osób albo jest przez nie kontrolowana. Możemy odmówić rejestracji lub zawiesić konto, które nie spełnia tego warunku, i
podamy powód.

**pt — Onde o KIFF está disponível.** Só pode usar o KIFF e o tafs nos países e regiões onde o Claude da Anthropic está
disponível, conforme a lista da Anthropic, e não enquanto se encontrar fisicamente noutro lugar. Não pode usá-los para,
ou em nome de, uma organização constituída ou com sede fora desses países e regiões, ou que seja detida
maioritariamente ou controlada, direta ou indiretamente, por uma organização desse tipo ou por pessoas aí localizadas.
Podemos recusar um registo, ou suspender uma conta, que não cumpra esta regra, e dizemos-lhe porquê.
