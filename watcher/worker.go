package main

import (
	"log"
	"os/exec"
	"sort"
	"strings"
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

func sessionPriority(rec sessionDiscovery) string {
	p := strings.ToLower(strings.TrimSpace(rec.State.Priority))
	switch p {
	case "urgent", "high", "medium", "low":
		return p
	default:
		return "low"
	}
}

func priorityRank(priority string) int {
	switch priority {
	case "urgent":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	default:
		return 3
	}
}

func reconcileWorkers(cfg Config) {
	if cfg.MaxWorkers <= 0 {
		return
	}
	sessions := discoverSessions(cfg.SessionDir)
	sort.Slice(sessions, func(i, j int) bool {
		ri := priorityRank(sessionPriority(sessions[i]))
		rj := priorityRank(sessionPriority(sessions[j]))
		if ri != rj {
			return ri < rj
		}
		return sessions[i].Number < sessions[j].Number
	})

	activeLimited := 0
	for _, rec := range sessions {
		priority := sessionPriority(rec)
		limited := priority != "urgent"

		if workerIsLive(cfg, rec) {
			if limited {
				activeLimited++
			}
			continue
		}
		if workerIdentityValid(cfg, rec) {
			if workerLockHeld(rec.LockPath) {
				log.Printf("unhealthy worker issue %d pid=%d; terminating", rec.Number, rec.State.Worker.PID)
				terminateWorkerPID(rec.State.Worker.PID)
			}
			if limited {
				activeLimited++
			}
			continue
		}
		if workerLockHeld(rec.LockPath) {
			if limited {
				activeLimited++
			}
			continue
		}
		if !sessionWorkPending(rec) {
			continue
		}
		if limited && activeLimited >= cfg.MaxWorkers {
			continue
		}
		wp, err := startWorkerProcessFn(cfg, rec.Number)
		if err != nil {
			log.Printf("start worker issue %d: %v", rec.Number, err)
			continue
		}
		if limited {
			activeLimited++
		}
		pid := 0
		if wp != nil && wp.cmd != nil && wp.cmd.Process != nil {
			pid = wp.cmd.Process.Pid
		}
		log.Printf("started worker issue %d pid=%d priority=%s", rec.Number, pid, priority)
	}
}
