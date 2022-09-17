package main

import (
	"debug/elf"
	"log"
)

func fileIsELFOrFatal(filePath string) {
	f, err := elf.Open(filePath)
	if err != nil {
		log.Fatalf("%s is not an ELF binary! %v (perhaps running into https://github.com/golang/go/issues/53804?)", filePath, err)
	}
	if err := f.Close(); err != nil {
		log.Fatalf("%s is not an ELF binary! Close: %v (perhaps running into https://github.com/golang/go/issues/53804?)", filePath, err)
	}
}
