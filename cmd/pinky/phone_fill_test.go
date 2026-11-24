package main

import (
	"bytes"
	"testing"
)

func TestPhoneAppOnScreen(t *testing.T) {
	m := phoneAppOnScreen.FindStringSubmatch("settled\nscreen 750x1334 · app: Home Screen (com.apple.springboard) · portrait")
	if m == nil || m[1] != "com.apple.springboard" {
		t.Fatalf("bundle id not read: %v", m)
	}
	if phoneAppOnScreen.MatchString("screen 750x1334 · portrait") {
		t.Fatal("matched a screen with no app")
	}
}

func TestPhoneFillNeedsLogin(t *testing.T) {
	var out, errb bytes.Buffer
	code := phoneMain([]string{"fill"}, phoneIO{out: &out, err: &errb})
	if code != phoneExitUsage {
		t.Fatalf("code %d", code)
	}
}
