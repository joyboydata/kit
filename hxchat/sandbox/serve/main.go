// Command serve boots the hxchat demo behind the kit sandbox harness.
// It prints the base URL and blocks until interrupted.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/joyboydata/kit/hxchat/sandbox/demo"
	sandbox "github.com/joyboydata/kit/sandbox"
)

func main() {
	store := demo.NewMemStore()
	mount, seeds := demo.Build(store)
	env, err := sandbox.Start(sandbox.Config{
		Source: sandbox.Static{
			Mount: mount,
			Seeds: seeds,
			Describe: func() any {
				return map[string]int{"t1": len(store.List("t1"))}
			},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		os.Exit(1)
	}
	defer env.Close()
	fmt.Println(env.BaseURL)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}
