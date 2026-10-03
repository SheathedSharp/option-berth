package ports

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkParseLsof measures the platform collector's text-to-listener
// conversion independently from spawning lsof. The fixture contains the same
// listener identity repeated across 16..1024 rows, including duplicate binds.
func BenchmarkParseLsof(b *testing.B) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("listeners=%d", n), func(b *testing.B) {
			var fixture strings.Builder
			fixture.WriteString("COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\n")
			for i := 0; i < n; i++ {
				fmt.Fprintf(&fixture, "node %d user 6u IPv4 1 0t0 TCP 127.0.0.1:%d (LISTEN)\n", i+1, 3000+i)
			}
			input := fixture.String()
			assertListenerBenchmark(b, parseLsof(input), n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = parseLsof(input)
			}
		})
	}
}

func BenchmarkParseSS(b *testing.B) {
	benchmarkListenerRows(b, parseSS,
		"State Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n",
		"LISTEN 0 128 127.0.0.1:%[2]d 0.0.0.0:* users:((\"node\",pid=%[1]d,fd=6))\n")
}

func BenchmarkParseNetstat(b *testing.B) {
	benchmarkListenerRows(b, parseNetstat,
		"Proto Local Address Foreign Address State PID\n",
		"TCP 127.0.0.1:%[2]d 0.0.0.0:0 LISTENING %[1]d\n")
}

func benchmarkListenerRows(b *testing.B, parse func(string) []ListeningPort, header, row string) {
	for _, n := range []int{16, 64, 256, 1024} {
		b.Run(fmt.Sprintf("listeners=%d", n), func(b *testing.B) {
			var fixture strings.Builder
			fixture.WriteString(header)
			for i := 0; i < n; i++ {
				fmt.Fprintf(&fixture, row, i+1, 3000+i)
			}
			input := fixture.String()
			assertListenerBenchmark(b, parse(input), n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = parse(input)
			}
		})
	}
}

func assertListenerBenchmark(b *testing.B, rows []ListeningPort, n int) {
	b.Helper()
	if len(rows) != n || rows[n-1].PID != n || rows[n-1].Port != 3000+n-1 {
		b.Fatalf("benchmark fixture did not produce %d complete listener facts", n)
	}
}
