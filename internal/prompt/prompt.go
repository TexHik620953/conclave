package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/domain"
)

const base = `You are Conclave, a terminal coding agent.
Be concise, accurate and act instead of narrating. Use tools when needed.
Never invent file contents, command output or test results.
Prefer small, focused changes and verify your work with tests where practical.
When you are done, give a short summary of what changed and what you verified.
`

const build = `You are in BUILD mode. You may read and modify the workspace, run commands and tests.
` + base

const plan = `You are in PLAN mode. You may read files and inspect the project, but you must NOT modify files,
run mutating shell commands or claim that changes were made.
You MAY create, edit, delete, link and comment on Board tasks. Use those tasks to capture the plan.
Produce a concrete implementation plan with affected files, dependencies, risks and verification.
Ask the user if a decision is genuinely ambiguous.
`

const master = `You are the Conclave planner and reviewer. You never implement code or run mutating shell commands.
Your job is to decompose work into persistent Board tasks, dispatch it, and review the result.

Planning rules:
1. Inspect the repository and the existing Board first. Avoid duplicate or overlapping tasks.
2. Decompose the goal into independently executable tasks, not dozens of tiny fragments.
3. Each task must have: title, description, context, acceptance criteria, complexity 1-5, priority 1-5,
   worker role and write scope. Declare dependencies when order matters.
4. Overlapping write scopes must not run in parallel.
5. Complexity selects the worker tier; priority only controls queue order.
6. Prefer 3-7 substantial tasks for a normal feature.
7. When ready, call submit_plan with the DAG. Do not create the same task twice.

Research requests:
- If the user asks to research, analyze, audit or understand the repository, you MUST decompose the work
  into 3-7 research tasks assigned to the "researcher" profile.
- Split by module, subsystem or concern. Add a final synthesis task depending on the research tasks.
- Do not perform the whole research yourself and do not finish before research tasks are complete.
Do not ask the user for worker profile IDs, model configuration or other internal settings:
use the AVAILABLE WORKER PROFILES provided in this prompt.
If you need information from the user, you MUST call the ask_user tool. Do not write questions as
plain chat text and then continue; ask_user blocks until the answer arrives.

Review rules:
- inspect the compact report and task diffs;
- accept completed work, reopen incomplete work with concrete feedback, or create follow-up tasks;
- call finish_run only when acceptance criteria are satisfied.
`

const worker = `You are a Conclave WORKER. Execute exactly one assigned task and nothing else.
You have an isolated git worktree. Stay inside write_scope unless the task explicitly requires otherwise.
Read the task context and acceptance criteria, inspect the relevant files, implement the change,
run the requested checks, and commit your work.
Log important decisions and test results. Create follow-up Board tasks for out-of-scope issues
instead of expanding the current task. Do not assign work to other agents directly.
Do not ask the user questions. If blocked, explain the blocker precisely in your final report.
`

const reviewer = `You are a Conclave VERIFIER. Inspect the completed work objectively.
Run the relevant tests and report failures, regressions and acceptance-criteria gaps.
Do not modify code.
`

const finishTool = `Report the final state, what was verified, and any remaining risks.`

func Mode(m domain.Mode) string {
	switch m {
	case domain.ModePlan:
		return plan
	case domain.ModeConclave:
		return master
	default:
		return build
	}
}

func Worker() string   { return worker }
func Reviewer() string { return reviewer }

// Build creates the system prompt for a session. Config prompt keys override
// the built-in mode prompt: "system", "build", "plan", "master", "worker",
// "reviewer". AGENTS.md is appended when present.
func Build(cfg *config.Config, profile config.Profile, mode domain.Mode, cwd, extra string) string {
	key := "build"
	text := build
	switch mode {
	case domain.ModePlan:
		key, text = "plan", plan
	case domain.ModeConclave:
		key, text = "master", master
	}
	if v := strings.TrimSpace(cfg.Prompts[key]); v != "" {
		text = v
	}
	parts := []string{text}
	if v := strings.TrimSpace(cfg.Prompts["system"]); v != "" {
		parts = append(parts, v)
	}
	parts = append(parts,
		fmt.Sprintf("Workspace: %s", cwd),
		fmt.Sprintf("Date: %s", time.Now().Format("2006-01-02")),
		"Platform: "+platform(),
	)
	if mode == domain.ModeConclave {
		parts = append(parts, "Profile: "+cfg.DefaultProfile)
		var workers []string
		for _, w := range profile.Workers {
			access := "write"
			for _, tool := range w.Tools {
				if tool == "write_file" || tool == "edit_file" || tool == "shell" {
					access = "write"
					break
				}
				access = "read-only"
			}
			workers = append(workers, fmt.Sprintf("- %s: model=%s effort=%s complexity=%v instances=%d access=%s", w.ID, w.Model, w.Effort, w.Complexity, w.Instances, access))
		}
		parts = append(parts, "AVAILABLE WORKER PROFILES (use these exact IDs for tasks; never ask the user for worker IDs):\n"+strings.Join(workers, "\n"))
	} else {
		parts = append(parts, "Model: "+profile.Agent.Model, "Effort: "+profile.Agent.Effort)
	}
	if extra != "" {
		parts = append(parts, extra)
	}
	if agents := readAgents(cwd); agents != "" {
		parts = append(parts, "Project instructions (AGENTS.md):\n"+agents)
	}
	return strings.Join(parts, "\n\n")
}

func WorkerSystem(cfg *config.Config, task domain.Task, w config.WorkerConfig, cwd string) string {
	text := worker
	if v := strings.TrimSpace(cfg.Prompts["worker"]); v != "" {
		text = v
	}
	if override := strings.TrimSpace(cfg.Prompts["worker."+w.ID]); override != "" {
		text = override
	}
	parts := []string{text,
		"Workspace: " + cwd,
		"Task ID: " + task.ID,
		"Title: " + task.Title,
		"Task spec:\n" + task.Spec,
	}
	if len(task.Acceptance) > 0 {
		parts = append(parts, "Acceptance criteria:\n- "+strings.Join(task.Acceptance, "\n- "))
	}
	if len(task.WriteScope) > 0 {
		parts = append(parts, "Write scope:\n- "+strings.Join(task.WriteScope, "\n- "))
	}
	if agents := readAgents(cwd); agents != "" {
		parts = append(parts, "Project instructions (AGENTS.md):\n"+agents)
	}
	return strings.Join(parts, "\n\n")
}

func ReviewSystem(cfg *config.Config, run *domain.Run) string {
	text := reviewer
	if v := strings.TrimSpace(cfg.Prompts["reviewer"]); v != "" {
		text = v
	}
	parts := []string{text, "Run goal: " + run.Goal, finishTool}
	return strings.Join(parts, "\n\n")
}

func readAgents(cwd string) string {
	b, err := os.ReadFile(filepath.Join(cwd, "AGENTS.md"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 12000 {
		s = s[:12000] + "\n...[truncated]"
	}
	return s
}

func platform() string {
	return fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
}
