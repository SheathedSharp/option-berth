package doctor

import "github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"

// A check reports three things and occasionally two more: what it saw, how bad
// it is, and what to do next. Writing that as a struct literal repeats
// `Status:` and `Summary:` on every branch of every check, which is how a
// reader loses the branch that actually differs. These constructors name the
// verdict, so the branches read as the sentences they produce.

// ok is a check that found what it expected.
func ok(summary, detail string) rpc.DoctorCheck {
	return rpc.DoctorCheck{Status: StatusOK, Summary: summary, Detail: detail}
}

// warn is a state that works but is not what this build expects.
func warn(summary, detail, fix string) rpc.DoctorCheck {
	return rpc.DoctorCheck{Status: StatusWarn, Summary: summary, Detail: detail, Fix: fix}
}

// fail is a state that breaks something a person is about to rely on.
func fail(summary, detail, fix string) rpc.DoctorCheck {
	return rpc.DoctorCheck{Status: StatusFail, Summary: summary, Detail: detail, Fix: fix}
}

// skip is a check that did not apply, with the reason a reader needs to decide
// whether it should have. It takes no fix: a skipped check is not a problem to
// repair.
func skip(summary, detail string) rpc.DoctorCheck {
	return rpc.DoctorCheck{Status: StatusSkip, Summary: summary, Detail: detail}
}

// skipWithFix is skip for a check that did not apply *yet* and whose fix is the
// way to make it apply — the optional model row, whose fix turns it on.
func skipWithFix(summary, detail, fix string) rpc.DoctorCheck {
	row := skip(summary, detail)
	row.Fix = fix
	return row
}

// warnFixable and failFixable mark the rows `oberth doctor --fix` may repair
// unattended. The flag is carried separately from the status because a warning
// can be repairable while a failure is not, and vice versa.
func warnFixable(summary, detail, fix string) rpc.DoctorCheck {
	return fixable(warn(summary, detail, fix))
}

func failFixable(summary, detail, fix string) rpc.DoctorCheck {
	return fixable(fail(summary, detail, fix))
}

func fixable(row rpc.DoctorCheck) rpc.DoctorCheck {
	row.Fixable = true
	return row
}
