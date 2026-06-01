package main

import (
	"fmt"

	"github.com/C5Hwang/singbox-deploy/internal/app"
)

func main() {
	info := app.Metadata()
	fmt.Printf("%s\n", info.Name)
}
