package model

import "testing"

func TestAssessmentContextBindsExactTargets(t *testing.T) {
	a := TaskContext{Task: "same", Services: []string{"api", "web"}, RunIDs: []string{"run-1"}}
	b := a
	b.Services = []string{"web", "api"}
	if assessmentContextID(a, nil) != assessmentContextID(b, nil) {
		t.Fatal("ordering changes task identity")
	}
	b.RunIDs = []string{"run-2"}
	if assessmentContextID(a, nil) == assessmentContextID(b, nil) {
		t.Fatal("replacement run reused assessment")
	}
	b = a
	b.Services = []string{"other"}
	if assessmentContextID(a, nil) == assessmentContextID(b, nil) {
		t.Fatal("other service reused assessment")
	}
}
