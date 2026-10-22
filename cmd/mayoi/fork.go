package main

// The fork-keeping commands, on the personal "mine" branch only and not
// for upstream: the git workflow of this fork — syncing sorrycc/Mayoi,
// branching for PRs, stacking and unstacking them, running the app —
// each run in a new terminal of the workspace, where the output and a
// conflict to resolve stay in view. forkScriptFor shares their scripts
// with the fork panel (forkpanel.go), which runs them headless.

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"mayoi/locales"
)

// forkScriptFor is the script of a fork action. arg and arg2 are the
// action's inputs, as the prompts or the panel fields give them:
// branch name and base for "feat", the dependency for "restack".
func forkScriptFor(cmd, arg, arg2 string) (string, error) {
	switch cmd {
	case "sync":
		return `prev="$(git branch --show-current)" && ` +
			`echo "== sync upstream into main" && ` +
			`git switch main && git pull && git push origin main && ` +
			`{ [ "$prev" = "main" ] || { git switch "$prev" && git merge main && ` +
			`{ ! git rev-parse --abbrev-ref --symbolic-full-name '@{u}' >/dev/null 2>&1 || git push; }; }; }`, nil
	case "status":
		return `git status -sb && echo && git log --oneline -8 && echo && git remote -v`, nil
	case "push":
		return `git push -u origin HEAD`, nil
	case "feat":
		name := strings.TrimSpace(arg)
		if !validBranch(name) {
			return "", errors.New(locales.BranchInvalidName())
		}
		if !strings.Contains(name, "/") {
			name = "feat/" + name
		}
		base := strings.TrimSpace(arg2)
		if base == "" {
			base = "main"
		} else if !validBranch(base) {
			return "", errors.New(locales.BranchInvalidName())
		}
		return fmt.Sprintf("git switch '%s' && git switch -c '%s' && git push -u origin '%s'", base, name, name), nil
	case "restack":
		dep := strings.TrimSpace(arg)
		if dep == "" {
			return `git rebase main && git push --force-with-lease`, nil
		}
		if !validBranch(dep) {
			return "", errors.New(locales.BranchInvalidName())
		}
		return fmt.Sprintf(`git rebase --onto main '%s' && git push --force-with-lease`, dep), nil
	case "dev":
		return `bash scripts/dev.sh`, nil
	case "build":
		return `go tool mygo build && rm -rf /Applications/Mayoi.app && ` +
			`cp -R build/darwin-arm64/Mayoi.app /Applications/ && ` +
			`echo && echo "== installed: /Applications/Mayoi.app" && open -R /Applications/Mayoi.app`, nil
	}
	return "", errors.New("unknown action")
}

// forkShell runs script in a new terminal of the workspace — the panel,
// or a tab of the editor area, as terminalLocation says — and gives it
// the keyboard.
func (a *app) forkShell(script string) {
	dir := a.workDir()
	s := a.startTerminal(dir)
	if s == nil {
		return
	}
	if a.settings.TerminalLocation == "editor" {
		col := a.fileColumn()
		s.tab = &colTab{term: s}
		a.addTab(col, col.active+1, s.tab)
		a.activate(col, col.active+1, true)
	} else {
		a.panelTerm = s
		a.window.PanelOpen, a.window.TerminalOpen = true, true
		a.settings.Changed()
		a.focusTerminal()
	}
	// Send, not Paste: the shells ask for bracketed pastes, which would
	// sit at the prompt until Enter.
	s.term.Send([]byte(script + "\n"))
}

// forkShellCmd runs the action of a Fork menu item in a terminal.
func (a *app) forkShellCmd(cmd string) {
	script, err := forkScriptFor(cmd, "", "")
	if err != nil {
		a.tell(note{message: err.Error()})
		return
	}
	a.forkShell(script)
}

func (a *app) forkSync()   { a.forkShellCmd("sync") }
func (a *app) forkStatus() { a.forkShellCmd("status") }
func (a *app) forkPush()   { a.forkShellCmd("push") }

// forkNewBranch asks the name of a branch to make — "feat/" is added
// without a slash — then the base to cut it from: main, or another
// branch to stack it on.
func (a *app) forkNewBranch() {
	a.prompt = &prompt{title: locales.ForkNewBranchTitle(), hint: locales.ForkNewBranchHint(), done: func(v string) string {
		v = strings.TrimSpace(v)
		if !validBranch(v) {
			return locales.BranchInvalidName()
		}
		if !strings.Contains(v, "/") {
			v = "feat/" + v
		}
		a.forkNewBranchBase(v)
		return ""
	}}
}

func (a *app) forkNewBranchBase(name string) {
	a.prompt = &prompt{title: locales.ForkBaseTitle(name), hint: locales.ForkBaseHint(), done: func(v string) string {
		v = strings.TrimSpace(v)
		if v != "" && !validBranch(v) {
			return locales.BranchInvalidName()
		}
		a.forkShellRun("feat", name, v)
		return ""
	}}
}

// forkShellRun runs the action with its arguments in a terminal.
func (a *app) forkShellRun(cmd, arg, arg2 string) {
	script, err := forkScriptFor(cmd, arg, arg2)
	if err != nil {
		a.tell(note{message: err.Error()})
		return
	}
	a.forkShell(script)
}

// forkRestack asks the dependency to detach the current branch from —
// empty once it has landed upstream, its commits then already in main —
// and replays the branch onto main, force-pushing it.
func (a *app) forkRestack() {
	a.prompt = &prompt{title: locales.ForkRestackTitle(), hint: locales.ForkRestackHint(), done: func(v string) string {
		v = strings.TrimSpace(v)
		if v != "" && !validBranch(v) {
			return locales.BranchInvalidName()
		}
		a.forkShellRun("restack", v, "")
		return ""
	}}
}

// forkGit runs git in the workspace and answers its stdout.
func (a *app) forkGit(args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", a.workDir()}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// forkRepo is the "owner/repo" origin pushes to.
func (a *app) forkRepo() string {
	u, _ := a.forkGit("remote", "get-url", "origin")
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimPrefix(u, "https://github.com/")
	u = strings.TrimPrefix(u, "git@github.com:")
	return u
}

// forkPR opens the compare page the current branch's PR is made from:
// into upstream's main for base "", into base of the own fork
// otherwise — the diff of a stacked PR while its dependency is pending.
func (a *app) forkPR(base string) {
	b, err := a.forkGit("branch", "--show-current")
	if err != nil || b == "" || b == "main" {
		a.tell(note{message: locales.ForkNotOnFeature()})
		return
	}
	var url string
	if repo := a.forkRepo(); base == "" {
		owner, _, _ := strings.Cut(repo, "/")
		url = fmt.Sprintf("https://github.com/sorrycc/Mayoi/compare/main...%s:%s", owner, b)
	} else {
		url = fmt.Sprintf("https://github.com/%s/compare/%s...%s", repo, base, b)
	}
	a.services.OpenURL(url)
}

func (a *app) forkPRUpstream() { a.forkPR("") }

// forkPRFork asks the branch the PR is based on in the own fork.
func (a *app) forkPRFork() {
	a.prompt = &prompt{title: locales.ForkPrBaseTitle(), hint: locales.ForkPrBaseHint(), done: func(v string) string {
		v = strings.TrimSpace(v)
		if !validBranch(v) {
			return locales.BranchInvalidName()
		}
		a.forkPR(v)
		return ""
	}}
}
