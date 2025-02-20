package main

import "github.com/jakobilobi/wadjit"

func main() {
	wadjit := wadjit.New()
	defer wadjit.Close()
}
