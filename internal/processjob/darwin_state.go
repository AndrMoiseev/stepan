package processjob

// Darwin exports these values in extern_proc through kern.proc sysctls.
// See xnu/bsd/sys/proc.h and fill_user64_externproc in kern_sysctl.c.
const (
	darwinZombie  = 5
	darwinExiting = 0x00002000 // P_WEXIT: kernel exit has begun, before SZOMB
)

func darwinProcessExited(status int8, flags int32) bool {
	return status == darwinZombie || flags&darwinExiting != 0
}
