package sessions

// Windows has no cheap equivalent of `ps`; the CIM query used elsewhere in the
// engine is too expensive to run before every command start. Detection here
// rests on the agent's own id variable and otherwise falls back to the parent
// pid, which ancestryID supplies without a process table.
func processTable() []Process { return nil }
