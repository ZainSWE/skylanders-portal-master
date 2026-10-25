//go:build !js

package main

import "testing"

func TestRemoteHostOK(t *testing.T) {
	for host, want := range map[string]bool{
		"192.168.1.20:47320": true, "10.0.0.5:47320": true, "equinox:47320": true, "equinox.local:47320": true,
		"evil.example.com:47320": false, "8.8.8.8:47320": false, "attacker.com": false,
	} {
		if got := remoteHostOK(host); got != want {
			t.Errorf("%s: %v", host, got)
		}
	}
}
