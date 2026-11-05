package ports

import (
	"strconv"
	"strings"
)

// cwdsFromLsof turns one `lsof -a -p <pids> -d cwd -Fpn` run into pid -> cwd.
// The field output is one record per line, `p<pid>` opening a process and
// `n<path>` giving its working directory:
//
//	p1234
//	n/Users/me/project
//
// The error matters only when lsof printed nothing. lsof can exit non-zero
// when one pid it was asked about has gone away — routine when the caller asks
// about every listener — while still printing every record it resolved.
// Dropping the batch on that error left every process without a cwd for that
// scan, and without a cwd there is no git root, so groups and names silently
// fell back for one tick.
//
// The parser lives outside the darwin-only file so it is tested everywhere,
// not only where lsof runs.
func cwdsFromLsof(out []byte, err error) map[int]string {
	cwds := make(map[int]string)
	if err != nil && len(out) == 0 {
		return cwds
	}

	owner := 0
	for _, record := range strings.Split(string(out), "\n") {
		if record == "" {
			continue
		}
		switch record[0] {
		case 'p':
			number, convErr := strconv.Atoi(record[1:])
			if convErr == nil && number > 0 {
				owner = number
			} else {
				owner = 0
			}
		case 'n':
			if owner != 0 {
				cwds[owner] = record[1:]
			}
		}
	}
	return cwds
}
