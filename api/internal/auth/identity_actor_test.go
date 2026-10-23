package auth

import "testing"

func TestIdentityActorID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   Identity
		want string
	}{
		{
			name: "api key uses credential id",
			id: Identity{
				Subject:  "user-who-minted-key",
				Method:   MethodAPIKey,
				APIKey:   true,
				Metadata: map[string]string{"api_key_id": "key-runtime-1"},
			},
			want: "key-runtime-1",
		},
		{
			name: "session uses human subject",
			id:   Identity{Subject: "user-1", Method: MethodSession},
			want: "user-1",
		},
		{
			name: "developer identity uses subject",
			id:   Identity{Subject: "developer-1", Method: MethodDeveloper},
			want: "developer-1",
		},
		{
			name: "synthetic api key falls back to subject",
			id:   Identity{Subject: "legacy-key-subject", Method: MethodAPIKey, APIKey: true},
			want: "legacy-key-subject",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.id.ActorID(); got != tc.want {
				t.Fatalf("ActorID() = %q, want %q", got, tc.want)
			}
		})
	}
}
