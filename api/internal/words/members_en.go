package words

// English membership emails: word for word as KIFF wrote them before they
// were translated.
func init() {
	en.Membership = Membership{
		AnAccount:      "a KIFF account",
		AccountNamed:   func(name string) string { return "the KIFF account its admins named “" + name + "”" },
		UnnamedAccount: "Unnamed account",
		InviteSubject: func(name string) string {
			if name == "" {
				return "You're invited to an account on KIFF"
			}
			return "You're invited to “" + name + "” on KIFF"
		},
		InviteIntro: func(by, account, role, what string) string {
			return by + " invited you to " + account + ", as " + role + ", so you can " + what + "."
		},
		InviteRoleWhat: map[string]string{
			"admin":    "run the account: people, keys, billing and policy, as well as Cards and domains",
			"editor":   "set what the account's agents may do: Cards, domains and tools",
			"approver": "answer the account's held actions",
			"viewer":   "see the account's agents, Cards and activity without changing anything",
		},
		RoleName:        map[string]string{"admin": "admin", "editor": "editor", "approver": "approver", "viewer": "viewer"},
		InviteRoleOther: "work on the account",
		InviteAccept:    func(url string) string { return "Sign in with any address and accept it here: " + url },
		InviteButton:    "Review and accept the invitation",
		InviteExpires:   func(when string) string { return "The invitation expires " + when + "." },
		InviteIgnore:    "If you weren't expecting this, ignore it; nothing happens unless you accept.",

		AnAdmin: "An admin",
		Someone: "Someone",
		AMember: "A member",

		PeopleLink:       "See the people on the account",
		NoticeWhy:        "You are told because you are an admin or approver of the account.",
		AuthoritySubject: func(role, account string) string { return "A new " + role + " on " + account },
		AuthorityInvited: func(who, role, account, by string) string {
			return who + " was invited as " + role + " on " + account + " by " + by + "."
		},
		AuthorityChanged: func(who, from, role, account, by string) string {
			return who + " was changed from " + from + " as " + role + " on " + account + " by " + by + "."
		},
		JoinedSubject: func(account string) string { return "Someone joined " + account },
		JoinedUnknown: func(invited, role, account string) string {
			return "The invitation sent to " + invited + " was accepted, and they joined " + account + " as " + role + "."
		},
		JoinedOther: func(invited, who, role, account string) string {
			return "The invitation sent to " + invited + " was accepted by " + who + ", a different address, and they joined " + account + " as " + role + "."
		},
		JoinedSelf: func(who, role, account string) string {
			return who + " accepted their invitation, and they joined " + account + " as " + role + "."
		},
		CardChangeSubject: "A Card was changed to let a held action through",
		CardChangeBody: func(who, agent, action, entity string) string {
			card := "a Card"
			if agent != "" {
				card = "a Card held by " + agent
			}
			out := who + " changed " + card + " so the held " + action
			if entity != "" {
				out += " on " + entity
			}
			return out + " could go through. Separation of duties is on, so this is a Card change, not an approval by an approver; it is recorded on the Card."
		},
		CardChangeCards: func(cards string) string { return "Card: " + cards + "." },
		SoDOffSubject:   "Separation of duties was turned off",
		SoDOffBody: func(who, account string) string {
			return who + " turned separation of duties off on " + account + ". Admins and editors can now answer held actions too."
		},

		YourPhone:     "Your phone",
		RelinkSubject: func(phone string) string { return phone + " can approve again, with a new key" },
		RelinkBody: func(phone string) string {
			return phone + " can approve again on KIFF, with a new key for its fingerprint or face. " +
				"You approved it in your browser just now. If that wasn't you, remove the device in KIFF and sign in again."
		},
		NewApproverSubject: func(phone string) string { return phone + " can approve on KIFF now" },
		NewApproverBody: func(phone string) string {
			return phone + " can approve on KIFF now, with its fingerprint or face. You approved it in your browser just now, while another device of yours already approves. If that wasn't you, remove the device in KIFF and change your password."
		},

		PackAskSubject:  "A request for a tafs pack",
		PackAskBody:     func(who, pack string) string { return who + " asks for a pack: " + pack + "." },
		RaiseAskSubject: "A request to raise the tafs spend limit",
		RaiseAskBody: func(who string) string {
			return who + " asks you to raise the spend limit, so the account's tafs can go on."
		},
		AskReason: func(reason string) string { return "Their reason: " + reason },
		PackAskHow: func(url string) string {
			return "Approve by adding the pack, or decline, in tafs or on kiff.dev: " + url
		},
		RaiseAskHow: "Raise it, or decline, in tafs.",
	}
}
