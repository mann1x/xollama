// Command cmd runs one council over the stub; it exists to measure the
// runner's binary size and dependency count.
package main

import (
	"context"
	"fmt"

	"councileval/council"
	"councileval/impl/baseline"
	"councileval/stub"
	"councileval/suite"
)

func main() {
	res, err := baseline.Runner{}.Run(context.Background(), council.Defaults(), &stub.Model{Tokens: 8},
		suite.Conv("Review the design: what are its weakest points?", 1024), func(council.Event) {})
	fmt.Println(res.Answer, err)
}
