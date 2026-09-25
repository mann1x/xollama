package main

import (
	"context"
	"fmt"

	"councileval/council"
	"councileval/impl/eino"
	"councileval/stub"
	"councileval/suite"
)

func main() {
	res, err := (&eino.Runner{}).Run(context.Background(), council.Defaults(), &stub.Model{Tokens: 8},
		suite.Conv("Review the design: what are its three weakest points?", 1024), func(council.Event) {})
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Answer)
}
