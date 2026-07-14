package sdk_test

import (
	"fmt"
	"net/http"

	"github.com/sohipan21/distributed-rate-limiter/pkg/sdk"
)

// wrapping any handler so requests are rate-limited by the service
func ExampleMiddleware() {
	client, err := sdk.Dial("localhost:9090")
	if err != nil {
		panic(err)
	}

	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "hello")
	})

	limited := sdk.Middleware(client)(app)
	http.Handle("/", limited)
}
