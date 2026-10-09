package processjob

import "testing"

func TestDarwinProcessExitStates(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int8
		flags  int32
		exited bool
	}{
		{"running", 2, 0, false},
		{"sleeping", 3, 0, false},
		{"stopped", 4, 0, false},
		{"unrelated flags", 2, 0x4, false},
		{"exiting before zombie", 2, 0x2000, true},
		{"exiting with other flags", 3, 0x2004, true},
		{"zombie", 5, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := darwinProcessExited(test.status, test.flags); got != test.exited {
				t.Fatalf("status=%d flags=%#x: exited=%v, want %v", test.status, test.flags, got, test.exited)
			}
		})
	}
}
