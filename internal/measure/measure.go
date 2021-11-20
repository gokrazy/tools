package measure

import (
	"fmt"
	"strings"
	"time"
)

func Interactively(status string) (done func(fragment string)) {
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
