// A tiny HTTP server that drops in the rate-limiter SDK. Point -limiter at a
// running service (e.g. localhost:9091 from docker compose) and hit /:
// requests pass through until the caller's limit is hit, then get a 429.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/sohipan21/distributed-rate-limiter/pkg/sdk"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	limiter := flag.String("limiter", "localhost:9090", "rate-limiter grpc address")
	flag.Parse()

	client, err := sdk.Dial(*limiter)
	if err != nil {
		log.Fatal(err)
	}

	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "hello, you are under the limit")
	})

	// identify callers by ?user= for an easy demo; real apps override the key func
	keyFunc := func(r *http.Request) sdk.Request {
		id := r.URL.Query().Get("user")
		if id == "" {
			id = r.RemoteAddr
		}
		return sdk.Request{Identity: id, Tier: r.Header.Get("X-Tier"), Endpoint: r.URL.Path}
	}

	handler := sdk.Middleware(client, sdk.WithKeyFunc(keyFunc))(app)
	log.Printf("protected server on %s, limiter at %s", *addr, *limiter)
	log.Fatal(http.ListenAndServe(*addr, handler))
}
