package main

import "testing"

// portOf feeds the "http://localhost{0}" templates. They hardcode the host on
// purpose: the config holds a bind address, and pasting it after "localhost"
// printed "http://localhost127.0.0.1:8088" for the value the README recommends
// for keeping the panel off the LAN.
//
// The template itself is not free to change either - a lang/*.toml sitting on
// disk wins over the copy embedded in the binary, so an upgrade that replaces
// only the executable keeps the previous wording. The argument was the safe part
// to fix.
func TestPortOf(t *testing.T) {
	cases := []struct{ addr, want string }{
		{":8088", ":8088"},            // default: every interface
		{"0.0.0.0:8088", ":8088"},     // explicit all-interfaces
		{"[::]:8088", ":8088"},        // IPv6 all-interfaces
		{"127.0.0.1:8088", ":8088"},   // README's LAN-off setting
		{"192.168.1.5:9000", ":9000"}, // a specific interface keeps its port
		{"localhost:9000", ":9000"},   // a hostname bind works the same way
		{"nocolon", "nocolon"},        // malformed: handed back unchanged
	}
	for _, c := range cases {
		if got := portOf(c.addr); got != c.want {
			t.Errorf("portOf(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}
