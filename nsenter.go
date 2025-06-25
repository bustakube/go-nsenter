package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

var (
	pid     int
	command string
	mntNs   bool
	utsNs   bool
	netNs   bool
	ipcNs   bool
	pidNs   bool
)

var nsMap = map[string]int{
	"mnt":  unix.CLONE_NEWNS,
	"net":  unix.CLONE_NEWNET,
	"ipc":  unix.CLONE_NEWIPC,
	"uts":  unix.CLONE_NEWUTS,
	"user": unix.CLONE_NEWUSER,
	"pid":  unix.CLONE_NEWPID,
}

func enterNamespace(pid int, nsType string) error {
	nsPath := fmt.Sprintf("/proc/%d/ns/%s", pid, nsType)
	fd, err := os.Open(nsPath)
	if err != nil {
		return fmt.Errorf("failed to open namespace %s: %v", nsType, err)
	}
	defer fd.Close()

	nsConst, ok := nsMap[nsType]
	if !ok {
		return fmt.Errorf("unsupported namespace: %s", nsType)
	}

	if nsType == "mnt" {
		if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
			return fmt.Errorf("unshare mnt before setns: %w", err)
		}
	}

	if err := unix.Setns(int(fd.Fd()), nsConst); err != nil {
		return fmt.Errorf("setns for %s failed: %v", nsType, err)
	}
	return nil
}

func main() {
	flag.IntVar(&pid, "target", -1, "Target process PID")
	flag.BoolVar(&mntNs, "mnt", false, "Enter mount namespace")
	flag.BoolVar(&utsNs, "uts", false, "Enter UTS namespace")
	flag.BoolVar(&netNs, "net", false, "Enter network namespace")
	flag.BoolVar(&ipcNs, "ipc", false, "Enter IPC namespace")
	flag.BoolVar(&pidNs, "pid", false, "Enter PID namespace")
	flag.Parse()

	var command string
	var args []string

	if flag.NArg() == 0 {
		command = os.Getenv("SHELL")
		if command == "" {
			command = "/bin/sh"
		}
	} else {
		command = flag.Arg(0)
		args = flag.Args()[1:]
	}

	if pid < 0 || command == "" {
		flag.Usage()
		os.Exit(1)
	}

	needsFork := false

	runtime.LockOSThread() // Critical: required for setns to work correctly
	defer runtime.UnlockOSThread()

	if utsNs {
		if err := enterNamespace(pid, "uts"); err != nil {
			log.Fatalf("Failed to enter uts namespace: %v", err)
		}
	}
	if netNs {
		if err := enterNamespace(pid, "net"); err != nil {
			log.Fatalf("Failed to enter net namespace: %v", err)
		}
	}
	if ipcNs {
		if err := enterNamespace(pid, "ipc"); err != nil {
			log.Fatalf("Failed to enter ipc namespace: %v", err)
		}
	}

	// If we switch our mnt namespace, entering the pid namespace will only work
	// if we open our file descriptor first.
	nsPath := fmt.Sprintf("/proc/%d/ns/pid", pid)
	pidFD, err := os.Open(nsPath)
	if err != nil {
		fmt.Println("open %s: %w", nsPath, err)
		os.Exit(1)
	}
	defer pidFD.Close()

	if mntNs {
		if err := enterNamespace(pid, "mnt"); err != nil {
			log.Fatalf("Failed to enter mnt namespace: %v", err)
		}
	}

	// PID namespace has to be last and has to have the fork.
	if pidNs {
		if err := unix.Setns(int(pidFD.Fd()), nsMap["pid"]); err != nil {
			println("setns for pid failed: %v", err)
			os.Exit(1)
		}
		needsFork = true
		if !mntNs {
			fmt.Println("For now, there's a strange bug - if you don't get a new mount namespace, ps and similar command do not work properly")
		}
	}

	// Run the command
	if needsFork {
		cmd := exec.Command(command, args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "exec failed: %v\n", err)
			os.Exit(1)
		}
		// Enter PID namespace *in child only*
		cmd.SysProcAttr = &unix.SysProcAttr{
			Cloneflags: unix.CLONE_NEWPID,
		}
	} else {
		// Replace the current process if no pid ns involved
		if err := unix.Exec(command, append([]string{command}, args...), os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "exec %s: %v\n", command, err)
			os.Exit(1)
		}

	}

}
