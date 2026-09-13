package main

import (
	"log"
	"os/exec"
	"sort"
)

type workerProcess struct {
	issue      int
	cmd        *exec.Cmd
	done       chan error
	stdoutPath string
	stderrPath string
	pgid       int
}

var execCommand = exec.Command
var startWorkerProcessFn = startWorkerProcess

func reconcileWorkers(cfg Config) {
	if cfg.MaxWorkers <= 0 {
		return
	}
	sessions := discoverSessions(cfg.SessionDir)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Number < sessions[j].Number
	})

	active := 0
	for _, rec := range sessions {
		if workerIsLive(cfg, rec) {
			active++
			continue
		}
		if workerIdentityValid(cfg, rec) {
			if workerLockHeld(rec.LockPath) {
				log.Printf("unhealthy worker issue %d pid=%d; terminating", rec.Number, rec.State.Worker.PID)
				terminateWorkerPID(rec.State.Worker.PID)
			}
			active++
			continue
		}
		if workerLockHeld(rec.LockPath) {
			active++
			continue
		}
		if !sessionWorkPending(rec) {
			continue
		}
		if active >= cfg.MaxWorkers {
			continue
		}
		wp, err := startWorkerProcessFn(cfg, rec.Number)
		if err != nil {
			log.Printf("start worker issue %d: %v", rec.Number, err)
			continue
		}
		active++
		pid := 0
		if wp != nil && wp.cmd != nil && wp.cmd.Process != nil {
			pid = wp.cmd.Process.Pid
		}
		log.Printf("started worker issue %d pid=%d", rec.Number, pid)
	}
}
