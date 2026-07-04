package main

import "testing"

func TestOriginAllowed(t *testing.T) {
	list := []string{"https://app.example.com", "https://*.mlxmesh.io", "http://localhost:5173"}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://app.example.com", true},               // exact
		{"https://APP.example.com", true},               // case-insensitive
		{"https://dash.mlxmesh.io", true},               // wildcard subdomain
		{"https://a.b.mlxmesh.io", true},                // deeper subdomain
		{"https://mlxmesh.io", false},                   // apex not matched by *.
		{"https://evilmlxmesh.io", false},               // suffix lookalike
		{"https://app.example.com.attacker.net", false}, // not a real match
		{"http://localhost:5173", true},                 // dev origin with port
		{"", false},                                     // no origin
	}
	for _, c := range cases {
		if got := originAllowed(c.origin, list); got != c.want {
			t.Errorf("originAllowed(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}
