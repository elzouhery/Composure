package main

import (
	"fmt"
	"github.com/elzouhery/composure/internal/hub"
)

func main() {
	cases := map[string]bool{
		"1.27rc2-alpine3.24": true, "3.19": false, "1.24-alpine": false,
		"edge": true, "tip-alpine": true, "20260805": false, "latest": true,
		"3.20.1-alpine": false, "2.0.0-beta1": true, "1.0.0-rc.1": true,
		"22-bookworm": false, "8.0-arch": false, "3.0alpha": true, "16.1": false,
	}
	fail := 0
	for tag, want := range cases {
		got := hub.IsUnstable(tag)
		mark := "ok"
		if got != want {
			mark = "FAIL"
			fail++
		}
		fmt.Printf("  %-22s unstable=%-5v want=%-5v %s\n", tag, got, want, mark)
	}
	fmt.Printf("\n%d failures\n", fail)
	fmt.Println("date tags:", hub.IsDateTag("20260805"), hub.IsDateTag("3.19"), hub.IsDateTag("2026-08-05"))
}
