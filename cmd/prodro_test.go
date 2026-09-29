package cmd

import "testing"

func TestProdROGate(t *testing.T) {
	for _, c := range []struct {
		name, db string
		auth     bool
		ttl      string
		code     string
	}{
		{"postgres untouched", "postgres", false, "", ""},
		{"none untouched", "none", false, "", ""},
		{"prod-ro without auth refused", "prod-ro", false, "", "PROD_RO_REQUIRES_AUTH"},
		{"prod-ro hours=1 ok", "prod-ro", true, "hours=1", ""},
		{"prod-ro minutes=30 ok", "prod-ro", true, "minutes=30", ""},
		{"prod-ro hours=2 refused", "prod-ro", true, "hours=2", "INVALID_AUTH_TTL"},
		{"prod-ro hours=1;minutes=1 refused", "prod-ro", true, "hours=1;minutes=1", "INVALID_AUTH_TTL"},
		{"long ttl fine without prod-ro", "postgres", true, "days=7", ""},
	} {
		e := prodROGate(c.db, c.auth, c.ttl)
		got := ""
		if e != nil {
			got = e.Code
		}
		if got != c.code {
			t.Errorf("%s: got %q, want %q", c.name, got, c.code)
		}
	}
}

func TestValidProdROURL(t *testing.T) {
	for s, want := range map[string]bool{
		"postgres://vibe_ro:pw@db-replica:5432/reporting": true,
		"postgresql://vibe_ro:pw@db-replica/reporting":    true,
		"postgres://vibe_ro@db-replica/reporting":         false, // no password
		"postgres://vibe_ro:pw@db-replica":                false, // no database
		"mysql://vibe_ro:pw@db-replica/reporting":         false,
		"postgres://vibe_ro:pw@db-replica/reporting\nX=1": false, // .env injection
		"postgres://vibe_ro:p$w@db-replica/reporting":     false, // compose interpolation
		"": false,
	} {
		if validProdROURL(s) != want {
			t.Errorf("validProdROURL(%q) != %v", s, want)
		}
	}
}
