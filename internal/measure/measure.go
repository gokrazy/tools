package measure

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
)

func Interactively(status string) (done func(fragment string)) {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return func(string) {}
	}
	status = "[" + status + "]"
	fmt.Print(status)
	start := time.Now()
	return func(fragment string) {
		build := time.Since(start)
		fmt.Printf("\r[done] in %.2fs%s"+strings.Repeat(" ", len(status))+"\n",
			build.Seconds(),
			fragment)
	}
}
