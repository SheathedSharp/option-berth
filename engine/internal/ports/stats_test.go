package ports

import "testing"

// Get-Process output is read through its named columns, so a Select-Object
// reorder cannot move a start time into the memory field.
func TestDecodeWindowsSampleReadsNamedColumns(t *testing.T) {
	const out = "\"Id\",\"CPU\",\"WorkingSet64\",\"ThreadCount\",\"StartTime\"\r\n" +
		"\"7276\",\"12.5\",\"104857600\",\"24\",\"2026-10-03T09:00:00.0000000+02:00\"\r\n"
	table := parseCSVTable(out)
	if len(table.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(table.rows))
	}
	pid, sample, ok := decodeWindowsSample(table, table.rows[0])
	if !ok || pid != 7276 {
		t.Fatalf("decodeWindowsSample = pid %d, ok %v; want 7276, true", pid, ok)
	}
	if sample.CPUPercent != 12.5 || sample.MemoryRSS != 104857600 || sample.ThreadCount != 24 {
		t.Errorf("sample = %+v, want cpu 12.5, rss 104857600, threads 24", sample)
	}
	if sample.State != "running" {
		t.Errorf("state = %q, want running", sample.State)
	}
	if sample.StartTime == "" || sample.StartedAt == "" {
		t.Errorf("start evidence dropped: %+v", sample)
	}
}

func TestDecodeWindowsSampleFindsColumnsByName(t *testing.T) {
	const reordered = "\"StartTime\",\"ThreadCount\",\"WorkingSet64\",\"CPU\",\"Id\"\r\n" +
		"\"2026-10-03T09:00:00.0000000+02:00\",\"8\",\"2048\",\"3.5\",\"42\"\r\n"
	table := parseCSVTable(reordered)
	if len(table.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(table.rows))
	}
	pid, sample, ok := decodeWindowsSample(table, table.rows[0])
	if !ok || pid != 42 {
		t.Fatalf("decodeWindowsSample = pid %d, ok %v; want 42, true", pid, ok)
	}
	if sample.CPUPercent != 3.5 || sample.MemoryRSS != 2048 || sample.ThreadCount != 8 {
		t.Errorf("reordered columns were misread: %+v", sample)
	}
}

// A pid alone identifies a process but observes nothing about it; writing
// zeroes would publish them as facts.
func TestDecodeWindowsSampleRejectsARowWithoutFacts(t *testing.T) {
	const out = "\"Id\",\"CPU\",\"WorkingSet64\",\"ThreadCount\",\"StartTime\"\r\n" +
		"\"42\",\"\",\"\",\"\",\"\"\r\n" +
		"\"43\",\"1.0\",\"4096\",\"4\",\"\"\r\n"
	table := parseCSVTable(out)
	if len(table.rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(table.rows))
	}
	if _, _, ok := decodeWindowsSample(table, table.rows[0]); ok {
		t.Fatal("a row carrying only a pid became a sample")
	}
	if pid, _, ok := decodeWindowsSample(table, table.rows[1]); !ok || pid != 43 {
		t.Fatalf("the row after the empty one was lost: pid %d, ok %v", pid, ok)
	}
}

// PowerShell may prefix the document with a #TYPE line; it is not a header.
func TestParseCSVTableSkipsTheTypeLine(t *testing.T) {
	const out = "#TYPE Microsoft.PowerShell.Commands.Internal.Format.FormatStartData\r\n" +
		"\"Id\",\"CPU\"\r\n\"7\",\"1.5\"\r\n"
	table := parseCSVTable(out)
	if !table.knows("id") {
		t.Fatalf("the #TYPE line was read as the header: %v", table.columns)
	}
	if len(table.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(table.rows))
	}
}
