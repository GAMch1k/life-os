// Command vapid-keys prints a new VAPID key pair for web push as .env lines
// to paste into the root .env (docs/services/notifications.md#web-push):
//
//	cd services/notifications && go run ./cmd/vapid-keys
//
// The private key is a secret: keep it in .env only, never commit it.
// Replacing the pair later makes every subscribed device turn notifications
// on again.
package main

import (
	"fmt"
	"os"

	"github.com/GAMch1k/life-os/services/notifications/internal/webpush"
)

func main() {
	public, private, err := webpush.GenerateKeys()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vapid-keys: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("LIFE_OS_WEBPUSH_VAPID_PUBLIC_KEY=%s\nLIFE_OS_WEBPUSH_VAPID_PRIVATE_KEY=%s\n", public, private)
}
